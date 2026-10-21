package platform

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"siagent/internal/channels"
)

// Channel adapters download inbound images themselves, because only they hold
// the vendor decryption key. The bytes are staged in a per-channel directory
// instead of a protocol frame: process protocol frames are capped at 256 KiB,
// while one picture may be several MiB. The inbox row records the staged names,
// and admission promotes them to ordinary chat attachments, so channels reuse
// the Web composer's image path: same content checks, same per-conversation
// limits, same visual-model gate and the same Web history rendering.

// stagedImage is one validated inbound image waiting for admission. Path is
// relative to the data directory; AttachmentID is set once the file has been
// promoted, so a deferred admission can retry without uploading twice.
type stagedImage struct {
	Name         string `json:"name"`
	Mime         string `json:"mime"`
	Path         string `json:"path"`
	AttachmentID string `json:"attachment_id,omitempty"`
	Size         int64  `json:"size"`
}

// maxStagedImageBytes bounds one staged file before validation, so a
// misbehaving adapter cannot make the platform read an enormous file. The
// administrator's configured image limit still decides what is accepted.
const maxStagedImageBytes = 64 << 20

// channelMediaDir is the private area of one channel. Adapters upload into
// "inbox"; accepted pictures are re-staged under "staged" until admission. The
// two are separate so the platform never confuses its own files with uploads.
func (s *Server) channelMediaDir(channelID string) string {
	return filepath.Join(s.store.directory, "channel-media", channelID)
}

// channelAdapterDir is the only place an adapter may write inbound media.
func (s *Server) channelAdapterDir(channelID string) string {
	return filepath.Join(s.channelMediaDir(channelID), "inbox")
}

// decodeStagedImages parses the staged list recorded on an inbox row.
func decodeStagedImages(raw string) ([]stagedImage, error) {
	if raw == "" || raw == "[]" {
		return []stagedImage{}, nil
	}
	var staged []stagedImage
	if err := json.Unmarshal([]byte(raw), &staged); err != nil {
		return nil, err
	}
	return staged, nil
}

// channelImageExt resolves the stored extension from detected content, so a
// vendor-supplied name can never choose the file type.
func channelImageExt(mimeType string) (string, bool) {
	extension, ok := imageMIMETypes[mimeType]
	return extension, ok
}

// channelImageName keeps a readable display name without trusting the vendor for
// a path or an unbounded length.
func channelImageName(name, extension string) string {
	name = strings.TrimSpace(filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	if name == "" || name == "." || name == ".." || len(name) > 200 {
		return "图片" + extension
	}
	return name
}

// consumedImage is one adapter image whose bytes have already been read and
// whose staging file has been removed. Keeping the bytes lets the platform hash
// and validate them without touching adapter files twice.
type consumedImage struct {
	Name string
	data []byte
}

// consumeAdapterImages reads every adapter image once and deletes the adapter
// file immediately, so a redelivered message cannot leave stale bytes behind and
// the deduplication fingerprint describes content rather than file names.
func (s *Server) consumeAdapterImages(channelID string, images []channels.IncomingImage) ([]consumedImage, error) {
	if len(images) == 0 {
		return nil, nil
	}
	if len(images) > s.attachmentLimits.MaxMessageCount {
		s.discardAdapterMedia(channelID, images)
		return nil, errors.New("单条消息图片数量超过上限。")
	}
	consumed := make([]consumedImage, 0, len(images))
	for _, image := range images {
		data, err := s.readAdapterImage(channelID, image)
		if err != nil {
			// Remaining files must not be left behind either.
			s.discardAdapterMedia(channelID, images)
			return nil, err
		}
		consumed = append(consumed, consumedImage{Name: image.Name, data: data})
	}
	return consumed, nil
}

// stageChannelImages validates consumed images and writes them into the channel
// staging area until admission promotes them to chat attachments.
func (s *Server) stageChannelImages(channelID, inboxID string, images []consumedImage) ([]stagedImage, error) {
	if len(images) == 0 {
		return []stagedImage{}, nil
	}
	staged := make([]stagedImage, 0, len(images))
	var total int64
	for index, image := range images {
		data := image.data
		if len(data) == 0 {
			s.discardStagedImages(staged)
			return nil, errors.New("渠道图片为空。")
		}
		if int64(len(data)) > s.attachmentLimits.MaxImageBytes {
			s.discardStagedImages(staged)
			return nil, &admissionError{413, fmt.Sprintf("单张图片不能超过 %d MiB，请压缩后重试。", s.attachmentLimits.MaxImageBytes>>20)}
		}
		detected := http.DetectContentType(data)
		extension, ok := channelImageExt(detected)
		if !ok {
			s.discardStagedImages(staged)
			return nil, &admissionError{415, "仅支持 PNG/JPEG/WebP/GIF 图片，请重新发送。"}
		}
		total += int64(len(data))
		if total > s.attachmentLimits.MaxMessageImageBytes {
			s.discardStagedImages(staged)
			return nil, &admissionError{413, "单条消息图片总大小超过上限，请减少或压缩图片后重试。"}
		}
		relative := filepath.Join("channel-media", channelID, "staged", fmt.Sprintf("%s-%d%s", inboxID, index, extension))
		target := filepath.Join(s.store.directory, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			s.discardStagedImages(staged)
			return nil, err
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			s.discardStagedImages(staged)
			return nil, err
		}
		staged = append(staged, stagedImage{Name: channelImageName(image.Name, extension), Mime: detected, Path: relative, Size: int64(len(data))})
	}
	return staged, nil
}

// readAdapterImage reads one adapter-supplied image. An adapter must declare a
// plain file name inside its own staging directory; inline base64 exists only for
// small test payloads.
func (s *Server) readAdapterImage(channelID string, image channels.IncomingImage) ([]byte, error) {
	if image.Data != "" && image.File != "" {
		return nil, errors.New("渠道图片不能同时提供内容和文件。")
	}
	if image.Data != "" {
		data, err := base64.StdEncoding.DecodeString(image.Data)
		if err != nil || len(data) > maxStagedImageBytes {
			return nil, errors.New("渠道图片内容无效。")
		}
		return data, nil
	}
	if image.File == "" {
		return nil, errors.New("渠道图片缺少内容。")
	}
	if image.File != filepath.Base(image.File) || strings.ContainsAny(image.File, "\\\x00") || image.File == "." || image.File == ".." {
		return nil, errors.New("渠道图片文件名无效。")
	}
	path := filepath.Join(s.channelAdapterDir(channelID), image.File)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("渠道图片文件不存在。")
	}
	defer os.Remove(path)
	if info.Size() > maxStagedImageBytes {
		return nil, errors.New("渠道图片过大。")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("渠道图片无法读取。")
	}
	return data, nil
}

// discardAdapterMedia removes the adapter files of one message that is rejected
// before staging.
func (s *Server) discardAdapterMedia(channelID string, images []channels.IncomingImage) {
	for _, image := range images {
		if image.File == "" || image.File != filepath.Base(image.File) || strings.ContainsAny(image.File, "\\\x00") {
			continue
		}
		_ = os.Remove(filepath.Join(s.channelAdapterDir(channelID), image.File))
	}
}

// discardStagedImages removes staged files. Deletion is idempotent so a failed
// admission can call it unconditionally.
func (s *Server) discardStagedImages(staged []stagedImage) {
	for _, item := range staged {
		_ = os.Remove(filepath.Join(s.store.directory, filepath.Clean(item.Path)))
	}
}

// promoteStagedImages turns staged files into attachments of the target
// conversation and records the result on the inbox row, so a deferred admission
// retries without uploading a picture twice. Limits are checked before any file
// moves, and every failure undoes its own work. Callers hold s.mu.
func (s *Server) promoteStagedImages(inboxID, conversationID string, staged []stagedImage) ([]string, error) {
	if len(staged) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(staged))
	var count int
	var used int64
	if err := s.store.db.QueryRow("SELECT count(*),coalesce(sum(size),0) FROM attachments WHERE conversation_id=?", conversationID).Scan(&count, &used); err != nil {
		return nil, err
	}
	pending := 0
	extra := int64(0)
	for _, item := range staged {
		if item.AttachmentID != "" {
			ids = append(ids, item.AttachmentID)
			continue
		}
		pending++
		extra += item.Size
	}
	if pending == 0 {
		return ids, nil
	}
	if count+pending > s.attachmentLimits.MaxConversationCount || used+extra > s.attachmentLimits.MaxConversationBytes {
		return nil, &admissionError{413, "该会话附件已达上限，请新建会话后重新发送图片。"}
	}
	if err := os.MkdirAll(attachmentDir(s.store.directory), 0700); err != nil {
		return nil, err
	}
	written := make([]string, 0, pending)
	inserted := make([]string, 0, pending)
	undo := func() {
		for _, path := range written {
			_ = os.Remove(path)
		}
		for _, attachmentID := range inserted {
			_, _ = s.store.db.Exec("DELETE FROM attachments WHERE id=? AND linked=0", attachmentID)
		}
	}
	for index := range staged {
		item := &staged[index]
		if item.AttachmentID != "" {
			continue
		}
		source := filepath.Join(s.store.directory, filepath.Clean(item.Path))
		attachmentID := id()
		relative := filepath.Join("attachments", attachmentID+filepath.Ext(item.Path))
		target := filepath.Join(s.store.directory, relative)
		if err := os.Rename(source, target); err != nil {
			// A rename can fail across filesystems; copy rather than lose the image.
			data, readErr := os.ReadFile(source)
			if readErr != nil {
				undo()
				return nil, readErr
			}
			if writeErr := os.WriteFile(target, data, 0600); writeErr != nil {
				undo()
				return nil, writeErr
			}
			_ = os.Remove(source)
		}
		written = append(written, target)
		if _, err := s.store.db.Exec("INSERT INTO attachments(id,conversation_id,name,mime,kind,size,path,extracted,created) VALUES(?,?,?,?,?,?,?,'',strftime('%s','now')*1000)", attachmentID, conversationID, item.Name, item.Mime, "image", item.Size, relative); err != nil {
			undo()
			return nil, err
		}
		inserted = append(inserted, attachmentID)
		item.AttachmentID = attachmentID
		item.Path = relative
		ids = append(ids, attachmentID)
	}
	encoded, err := json.Marshal(staged)
	if err != nil {
		undo()
		return nil, err
	}
	if _, err := s.store.db.Exec("UPDATE channel_inbox SET attachments=? WHERE id=?", string(encoded), inboxID); err != nil {
		undo()
		return nil, err
	}
	return ids, nil
}

// inboxStagedImages reads the images staged for one inbox row.
func (s *Server) inboxStagedImages(inboxID string) ([]stagedImage, error) {
	var raw string
	if err := s.store.db.QueryRow("SELECT attachments FROM channel_inbox WHERE id=?", inboxID).Scan(&raw); err != nil {
		if err == sql.ErrNoRows {
			return []stagedImage{}, nil
		}
		return nil, err
	}
	return decodeStagedImages(raw)
}

// resetChannelMedia clears both the adapter upload area and the platform staging
// area of one channel. A closed adapter cannot be waiting for an answer and a
// restart rejects every pending message, so nothing here can still be promoted.
func (s *Server) resetChannelMedia(channelID string) {
	for _, directory := range []string{s.channelAdapterDir(channelID), filepath.Join(s.channelMediaDir(channelID), "staged")} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			_ = os.Remove(filepath.Join(directory, entry.Name()))
		}
	}
}

// cleanupChannelMedia clears every channel media area at startup.
func (s *Server) cleanupChannelMedia() {
	root := filepath.Join(s.store.directory, "channel-media")
	directories, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, directory := range directories {
		if directory.IsDir() {
			s.resetChannelMedia(directory.Name())
		}
	}
}

// discardInboundAttachments releases attachments created for a run that was
// rejected. Only unlinked rows are removed, so a committed run is safe.
func (s *Server) discardInboundAttachments(ids []string) {
	for _, attachmentID := range ids {
		var path string
		if err := s.store.db.QueryRow("SELECT path FROM attachments WHERE id=? AND linked=0", attachmentID).Scan(&path); err != nil {
			continue
		}
		if _, err := s.store.db.Exec("DELETE FROM attachments WHERE id=? AND linked=0", attachmentID); err != nil {
			continue
		}
		_ = os.Remove(filepath.Join(s.store.directory, filepath.Clean(path)))
	}
}
