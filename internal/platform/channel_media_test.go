package platform

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"siagent/internal/channels"
)

// pixelPNG is a real one-pixel PNG so content detection, not the file name,
// decides whether an inbound image is accepted.
const pixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

// imageChannelPlugin declares inbound image support and stages files exactly as
// the bundled WeCom bot does. It lets the platform path be tested without a
// vendor network.
type imageChannelPlugin struct {
	mediaDir string
	sent     []channels.Delivery
}

func (*imageChannelPlugin) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "fixture-image", Name: "Fixture image", Transport: "websocket", Streaming: true, Images: true, MaxTextBytes: 4096, IdentityFields: []string{"account"}, Fields: []channels.Field{
		{Key: "account", Label: "Account", Required: true},
		{Key: "token", Label: "Token", Secret: true, Required: true},
	}}
}
func (*imageChannelPlugin) Validate(channels.Config) error { return nil }
func (p *imageChannelPlugin) Open(_ context.Context, c channels.Config, _ channels.Receive) (channels.Connection, error) {
	p.mediaDir = c.MediaDir
	return p, nil
}
func (p *imageChannelPlugin) Send(_ context.Context, d channels.Delivery) channels.Result {
	p.sent = append(p.sent, d)
	return channels.Result{State: "sent"}
}
func (*imageChannelPlugin) Status() channels.Status { return channels.Status{State: "connected"} }
func (*imageChannelPlugin) Close()                  {}

func newImageChannel(t *testing.T, f *fixture) (Channel, *imageChannelPlugin) {
	t.Helper()
	plugin := &imageChannelPlugin{}
	if err := f.server.ConfigureChannelPlugins("node", "unused", []channels.Plugin{
		func(channels.PluginOptions) (channels.Provider, error) { return plugin, nil },
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.server.closeChannelConnections)
	response := f.request(t, "POST", "/api/channels", map[string]any{
		"provider": "fixture-image", "config": map[string]string{"account": "member"},
		"secrets": map[string]string{"token": "channel-secret"}, "agent_id": "default", "enabled": true, "confirm": true,
	}, true, nil)
	requireStatus(t, response, 201)
	var c Channel
	if err := json.Unmarshal(response.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	// A real connection is what gives the adapter its staging directory.
	if err := f.server.syncChannelConnections(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, plugin
}

// stageFile writes an adapter file the way the WeCom worker does before it
// reports an inbound frame.
func stageFile(t *testing.T, plugin *imageChannelPlugin, name string, data []byte) string {
	t.Helper()
	if plugin.mediaDir == "" {
		t.Fatal("adapter did not receive a staging directory")
	}
	if err := os.MkdirAll(plugin.mediaDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin.mediaDir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}

func visionModel(t *testing.T, f *fixture) {
	t.Helper()
	requireStatus(t, f.request(t, "PUT", "/api/model", map[string]any{
		"base_url": "http://localhost:9999/v1", "name": "fixture", "api_key": "secret",
		"options": map[string]any{"input": []string{"text", "image"}},
	}, true, nil), 200)
}

func TestChannelImageBecomesAttachmentAndReachesRuntime(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	visionModel(t, f)
	c, plugin := newImageChannel(t, f)
	png, err := base64.StdEncoding.DecodeString(pixelPNG)
	if err != nil {
		t.Fatal(err)
	}
	file := stageFile(t, plugin, "upload-1.bin", png)
	in := channels.Incoming{
		ID: "image-message", ChatType: "single", SenderID: "member", Kind: "image",
		Images:       []channels.IncomingImage{{Name: "屏幕截图.png", File: file}},
		ReplyContext: map[string]string{"req_id": "req-image"},
	}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
		t.Fatal(err)
	}
	// The adapter file is consumed, and the inbox row still describes the picture.
	if _, err := os.Stat(filepath.Join(plugin.mediaDir, file)); !os.IsNotExist(err) {
		t.Fatal("staged adapter file was not consumed")
	}
	var state, raw string
	if err := f.store.db.QueryRow("SELECT state,attachments FROM channel_inbox WHERE msg_id='image-message'").Scan(&state, &raw); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || !strings.Contains(raw, `"mime":"image/png"`) {
		t.Fatalf("image message not staged: %s %s", state, raw)
	}
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.runtime.requests) != 1 {
		t.Fatalf("image run was not admitted: %d", len(f.runtime.requests))
	}
	attachments, _ := f.runtime.requests[0]["attachments"].([]map[string]any)
	if len(attachments) != 1 || attachments[0]["kind"] != "image" || attachments[0]["mime"] != "image/png" || attachments[0]["data"] == "" {
		t.Fatalf("image did not reach the runtime: %#v", attachments)
	}
	if f.runtime.requests[0]["input"] != "请查看图片。" {
		t.Fatalf("picture-only message lost its prompt: %v", f.runtime.requests[0]["input"])
	}
	// The picture is an ordinary attachment of the channel conversation, so the
	// Web history renders it like a Web upload.
	var conversationID, messageRow string
	if err := f.store.db.QueryRow("SELECT conversation_id FROM channel_inbox WHERE msg_id='image-message'").Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	var mime, kind, path string
	var linked int
	if err := f.store.db.QueryRow("SELECT mime,kind,path,linked FROM attachments WHERE conversation_id=?", conversationID).Scan(&mime, &kind, &path, &linked); err != nil {
		t.Fatal(err)
	}
	if kind != "image" || mime != "image/png" || linked != 1 {
		t.Fatalf("attachment is not linked to the run: %s %s %d", kind, mime, linked)
	}
	if _, err := os.Stat(filepath.Join(f.store.directory, filepath.Clean(path))); err != nil {
		t.Fatal("attachment file missing", err)
	}
	if err := f.store.db.QueryRow("SELECT attachments FROM messages WHERE conversation_id=? AND role='user'", conversationID).Scan(&messageRow); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messageRow, `"kind":"image"`) {
		t.Fatalf("history does not list the picture: %s", messageRow)
	}
}

func TestChannelImageWithoutImageCapableModelIsRejectedWithCause(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	c, plugin := newImageChannel(t, f)
	png, _ := base64.StdEncoding.DecodeString(pixelPNG)
	file := stageFile(t, plugin, "upload-2.bin", png)
	in := channels.Incoming{ID: "image-rejected", ChatType: "single", SenderID: "member", Kind: "image", Images: []channels.IncomingImage{{Name: "a.png", File: file}}}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
		t.Fatal(err)
	}
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := f.store.db.QueryRow("SELECT state,error FROM channel_inbox WHERE msg_id='image-rejected'").Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "rejected" || !strings.Contains(reason, "图片输入") {
		t.Fatalf("missing model capability was not reported: %s %q", state, reason)
	}
	var attachments int
	if err := f.store.db.QueryRow("SELECT count(*) FROM attachments").Scan(&attachments); err != nil || attachments != 0 {
		t.Fatalf("rejected picture left an attachment: %d %v", attachments, err)
	}
	if len(f.runtime.requests) != 0 {
		t.Fatal("rejected picture created a run")
	}
}

func TestChannelImageHonorsAgentSwitchAndAdapterCapability(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	visionModel(t, f)
	c, plugin := newImageChannel(t, f)
	png, _ := base64.StdEncoding.DecodeString(pixelPNG)

	// The administrator switch wins over an otherwise capable setup.
	policy := defaultAgentPolicy()
	policy.Session.Images = false
	setTestPolicy(t, f, policy)
	file := stageFile(t, plugin, "switch.bin", png)
	in := channels.Incoming{ID: "switch-off", ChatType: "single", SenderID: "member", Kind: "image", Images: []channels.IncomingImage{{Name: "a.png", File: file}}}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := f.store.db.QueryRow("SELECT state,error FROM channel_inbox WHERE msg_id='switch-off'").Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "rejected" || !strings.Contains(reason, "关闭图片接收") {
		t.Fatalf("disabled image switch accepted a picture: %s %q", state, reason)
	}
	// The staged file is removed, so a disabled switch leaks nothing on disk.
	if _, err := os.Stat(filepath.Join(plugin.mediaDir, file)); !os.IsNotExist(err) {
		t.Fatal("rejected picture left its staged file")
	}
	// Text still works with images disabled.
	policy.Session.Images = false
	setTestPolicy(t, f, policy)
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "text-ok", ChatType: "single", SenderID: "member", Kind: "text", Text: "文字问题"}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.runtime.requests) != 1 {
		t.Fatalf("text message was blocked by the image switch: %d", len(f.runtime.requests))
	}
}

func TestChannelImageRejectsOversizedAndNonImageContent(t *testing.T) {
	f := newFixture(t, ServerOptions{Attachments: AttachmentLimits{MaxImageBytes: 1024, MaxMessageImageBytes: 2048}})
	f.setup(t)
	f.model(t, "fixture", "secret")
	visionModel(t, f)
	c, plugin := newImageChannel(t, f)

	big := stageFile(t, plugin, "big.bin", bytes.Repeat([]byte{0x89}, 4096))
	// A PNG header keeps detection honest while the size limit is what rejects it.
	bigData := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 4096)...)
	if err := os.WriteFile(filepath.Join(plugin.mediaDir, big), bigData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "too-big", ChatType: "single", SenderID: "member", Kind: "image", Images: []channels.IncomingImage{{Name: "big.png", File: big}}}); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := f.store.db.QueryRow("SELECT state,error FROM channel_inbox WHERE msg_id='too-big'").Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "rejected" || !strings.Contains(reason, "MiB") {
		t.Fatalf("oversized image accepted: %s %q", state, reason)
	}

	text := stageFile(t, plugin, "text.bin", []byte("这不是图片"))
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "not-image", ChatType: "single", SenderID: "member", Kind: "image", Images: []channels.IncomingImage{{Name: "x.png", File: text}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.db.QueryRow("SELECT state,error FROM channel_inbox WHERE msg_id='not-image'").Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "rejected" || !strings.Contains(reason, "PNG/JPEG/WebP/GIF") {
		t.Fatalf("non-image content accepted: %s %q", state, reason)
	}
	var attachments int
	if err := f.store.db.QueryRow("SELECT count(*) FROM attachments").Scan(&attachments); err != nil || attachments != 0 {
		t.Fatalf("rejected images left attachments: %d %v", attachments, err)
	}
}

func TestChannelImageRejectsPathTraversal(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	visionModel(t, f)
	c, plugin := newImageChannel(t, f)
	secret := filepath.Join(f.directory, "master.key")
	if _, err := os.Stat(secret); err != nil {
		t.Skip("fixture has no master key to point at")
	}
	// A frame naming a path outside its own staging directory is a misbehaving
	// adapter, so the whole frame is refused: it never becomes an inbox row and the
	// platform never reads the referenced file.
	for _, name := range []string{"../master.key", "sub/file.png", "/etc/passwd"} {
		in := channels.Incoming{ID: "escape-" + strings.ReplaceAll(name, "/", "_"), ChatType: "single", SenderID: "member", Kind: "image",
			Images: []channels.IncomingImage{{Name: "x.png", File: name}}}
		if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err == nil {
			t.Fatalf("path %q was accepted", name)
		}
	}
	var rows int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_inbox WHERE channel_id=?", c.ID).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("rejected frames were persisted: %d %v", rows, err)
	}
	if _, err := os.Stat(secret); err != nil {
		t.Fatal("staging read outside its directory", err)
	}
	_ = plugin
}

func TestChannelImageReplayWithDifferentPictureConflicts(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	visionModel(t, f)
	c, plugin := newImageChannel(t, f)
	png, _ := base64.StdEncoding.DecodeString(pixelPNG)
	first := stageFile(t, plugin, "replay-1.bin", png)
	in := channels.Incoming{ID: "replay", ChatType: "single", SenderID: "member", Kind: "image", Images: []channels.IncomingImage{{Name: "a.png", File: first}}}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
		t.Fatal(err)
	}
	// The same vendor message ID with a different picture must not be accepted.
	other := append([]byte{}, png...)
	in.Images = []channels.IncomingImage{{Name: "b.png", Data: base64.StdEncoding.EncodeToString(other) + "AA=="}}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err == nil {
		t.Fatal("conflicting picture replay was accepted")
	}
	// An identical replay is idempotent and does not stage a second file.
	same := channels.Incoming{ID: "replay", ChatType: "single", SenderID: "member", Kind: "image", Images: []channels.IncomingImage{{Name: "a.png", File: stageFile(t, plugin, "replay-2.bin", png)}}}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, same); err != nil {
		t.Fatal("identical replay rejected:", err)
	}
	var count int
	if err := f.store.db.QueryRow("SELECT count(*) FROM channel_inbox WHERE channel_id=?", c.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replay created a second message: %d %v", count, err)
	}
	// Both adapter uploads are consumed, including the one from the conflicting
	// and the identical replay. The picture of the accepted message stays staged
	// until admission promotes it.
	entries, err := os.ReadDir(plugin.mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("adapter uploads were not consumed: %d", len(entries))
	}
	staged, err := os.ReadDir(filepath.Join(f.directory, "channel-media", c.ID, "staged"))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 1 {
		t.Fatalf("accepted picture staging count is wrong: %d", len(staged))
	}
}

func TestChannelImageAdmissionDefersWithoutLosingPictures(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	visionModel(t, f)
	c, plugin := newImageChannel(t, f)
	png, _ := base64.StdEncoding.DecodeString(pixelPNG)
	file := stageFile(t, plugin, "defer.bin", png)
	in := channels.Incoming{ID: "defer", ChatType: "single", SenderID: "member", Kind: "image", Images: []channels.IncomingImage{{Name: "a.png", File: file}}}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
		t.Fatal(err)
	}
	// A not-ready runtime defers admission rather than rejecting it.
	f.server.ready = func() bool { return false }
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.store.db.QueryRow("SELECT state FROM channel_inbox WHERE msg_id='defer'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("deferred admission changed state: %s", state)
	}
	f.server.ready = func() bool { return true }
	if err := f.server.admitChannelInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.runtime.requests) != 1 {
		t.Fatalf("deferred message never admitted: %d", len(f.runtime.requests))
	}
	attachments, _ := f.runtime.requests[0]["attachments"].([]map[string]any)
	if len(attachments) != 1 {
		t.Fatalf("deferred picture was lost: %#v", attachments)
	}
	var rows int
	if err := f.store.db.QueryRow("SELECT count(*) FROM attachments").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("deferred picture was uploaded twice: %d %v", rows, err)
	}
}

func TestChannelWithoutImageCapabilityKeepsTextNotice(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	visionModel(t, f)
	// wecom-app is the bundled text-only adapter.
	c := createChannel(t, f, true)
	in := channels.Incoming{ID: "app-image", ChatType: "single", SenderID: "member", Kind: "image", Images: []channels.IncomingImage{{Name: "a.png", Data: pixelPNG}}}
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	if err := f.store.db.QueryRow("SELECT state,error FROM channel_inbox WHERE msg_id='app-image'").Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "rejected" || !strings.Contains(reason, "适配器不能接收图片") {
		t.Fatalf("text-only adapter accepted a picture: %s %q", state, reason)
	}
}

func TestChannelMediaIsPurgedOnStartupAndArchive(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	c, plugin := newImageChannel(t, f)
	directory := filepath.Join(f.directory, "channel-media", c.ID)
	if err := os.MkdirAll(plugin.mediaDir, 0700); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(plugin.mediaDir, "crash-remnant.bin")
	if err := os.WriteFile(leftover, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	// A restart rejects every pending message, so staged files cannot be promoted.
	f.server.cleanupChannelMedia()
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatal("startup cleanup kept a staged file")
	}
	// Archiving releases staged files of messages that will never run.
	png, _ := base64.StdEncoding.DecodeString(pixelPNG)
	file := stageFile(t, plugin, "archive.bin", png)
	if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, channels.Incoming{ID: "archive-me", ChatType: "single", SenderID: "member", Kind: "image", Images: []channels.IncomingImage{{Name: "a.png", File: file}}}); err != nil {
		t.Fatal(err)
	}
	// The platform re-stages an accepted picture under its own name.
	if entries, err := os.ReadDir(filepath.Join(directory, "staged")); err != nil || len(entries) != 1 {
		t.Fatalf("test setup did not stage the second picture: %v %v", err, entries)
	}
	// Disable first: archiving requires a stopped channel.
	disable := map[string]any{"name": c.Name, "provider": c.Provider, "config": c.Config, "agent_id": c.AgentID, "enabled": false}
	requireStatus(t, f.request(t, "PUT", "/api/channels/"+c.ID, disable, true, nil), 200)
	requireStatus(t, f.request(t, "DELETE", "/api/channels/"+c.ID, nil, true, nil), 200)
	for _, area := range []string{"inbox", "staged"} {
		entries, err := os.ReadDir(filepath.Join(directory, area))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("archiving kept files in %s: %d", area, len(entries))
		}
	}
}

func TestChannelInboxAttachmentsColumnDefaultsToEmpty(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	// Older inbox rows carry no staging list; decoding must be safe for them.
	var raw string
	if err := f.store.db.QueryRow("SELECT attachments FROM channel_inbox LIMIT 1").Scan(&raw); err != nil && err != sql.ErrNoRows {
		// The column exists and has a NOT NULL default, which is what matters.
		t.Fatal(err)
	}
	images, err := decodeStagedImages("")
	if err != nil || len(images) != 0 {
		t.Fatalf("empty staging list decoded incorrectly: %v %v", images, err)
	}
	images, err = decodeStagedImages("[]")
	if err != nil || len(images) != 0 {
		t.Fatalf("empty JSON list decoded incorrectly: %v %v", images, err)
	}
}

// TestChannelAdapterStagesImagesThroughTheProcessProtocol proves the protocol
// boundary end to end: a process adapter downloads a picture, writes it to the
// staging directory it was given, and the platform promotes it to an attachment
// without the bytes ever travelling inside a 256 KiB frame.
func TestChannelAdapterStagesImagesThroughTheProcessProtocol(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node required")
	}
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	visionModel(t, f)
	// A local HTTP server stands in for the vendor's expiring image URL.
	png, _ := base64.StdEncoding.DecodeString(pixelPNG)
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	}))
	defer imageServer.Close()
	directory := filepath.Join(f.directory, "channel-adapters", "image-adapter")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{"v": 1, "entry": "worker.mjs", "provider": channels.Descriptor{
		ID: "fixture-process-image", Name: "Fixture process image", Transport: "websocket", Images: true,
		MaxTextBytes: 1024, IdentityFields: []string{"account"},
		Fields: []channels.Field{{Key: "account", Label: "Account", Required: true}, {Key: "token", Label: "Token", Required: true, Secret: true}},
	}}
	raw, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(directory, "adapter.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The worker writes the picture into media_dir and reports only its name, which
	// is what the bundled WeCom worker does after downloading a message image.
	worker := `import{createInterface}from'node:readline';import{writeFile}from'node:fs/promises';import{join}from'node:path';const output=x=>process.stdout.write(JSON.stringify({v:1,...x})+'\n');let dir='';const PNG=Buffer.from(` + "`" + pixelPNG + "`" + `,'base64');const lines=createInterface({input:process.stdin});lines.on('line',async line=>{const f=JSON.parse(line);if(f.type==='configure'){dir=f.config.media_dir;output({type:'status',state:'connected'});await writeFile(join(dir,'photo.bin'),PNG);output({type:'incoming',incoming:{id:'process-image',chat_type:'single',sender_id:'member',kind:'image',text:'',images:[{name:'photo.png',file:'photo.bin'}],reply_context:{opaque:'fixture'}}});}if(f.type==='send')output({type:'result',id:f.id,result:{state:'sent',code:0}});});lines.on('close',()=>process.stdin.destroy());`
	if err := os.WriteFile(filepath.Join(directory, "worker.mjs"), []byte(worker), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.server.ConfigureChannelAdapters(node, "runtime/dist/channel-wecom.js"); err != nil {
		t.Fatal(err)
	}
	defer f.server.closeChannelConnections()
	response := f.request(t, "POST", "/api/channels", map[string]any{"provider": "fixture-process-image", "config": map[string]string{"account": "one"}, "secrets": map[string]string{"token": "private-token"}, "agent_id": "default", "enabled": true, "confirm": true}, true, nil)
	requireStatus(t, response, 201)
	var c Channel
	if err := json.Unmarshal(response.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for deadline := time.Now().Add(10 * time.Second); len(f.runtime.requests) < 1 && time.Now().Before(deadline); {
		if err := f.server.ChannelTick(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(f.runtime.requests) != 1 {
		t.Fatal("process adapter image was not admitted")
	}
	attachments, _ := f.runtime.requests[0]["attachments"].([]map[string]any)
	if len(attachments) != 1 || attachments[0]["kind"] != "image" {
		t.Fatalf("process adapter image did not reach the runtime: %#v", attachments)
	}
	// The adapter upload area is empty again once the platform consumed the file.
	entries, err := os.ReadDir(filepath.Join(f.directory, "channel-media", c.ID, "inbox"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("adapter upload area was not cleared: %d", len(entries))
	}
}

// TestChannelNonTextKindsKeepTheScopeNotice guards the boundary the image
// feature could blur: a file or video message must not slip through just because
// it happens to carry a caption.
func TestChannelNonTextKindsKeepTheScopeNotice(t *testing.T) {
	f := newFixture(t)
	f.setup(t)
	f.model(t, "fixture", "secret")
	visionModel(t, f)
	c := createChannel(t, f, true)
	for _, kind := range []string{"file", "video", "unsupported"} {
		in := channels.Incoming{ID: "kind-" + kind, ChatType: "single", SenderID: "member", Kind: kind, Text: "带说明的文件"}
		if err := f.server.receiveChannel(context.Background(), c.ID, c.Version, in); err != nil {
			t.Fatal(err)
		}
		var state, reason string
		if err := f.store.db.QueryRow("SELECT state,error FROM channel_inbox WHERE msg_id=?", in.ID).Scan(&state, &reason); err != nil {
			t.Fatal(err)
		}
		if state != "rejected" || !strings.Contains(reason, "文字和图片") {
			t.Fatalf("%s message was not rejected: %s %q", kind, state, reason)
		}
	}
	if len(f.runtime.requests) != 0 {
		t.Fatal("unsupported kinds created runs")
	}
}
