// Package weixin implements the Tencent ilink bot gateway used by personal
// WeChat: QR login, long polling for inbound messages and HTTP sends. It is the
// protocol behind OpenClaw's `openclaw-weixin`; there is no public callback and
// no public IP requirement.
package weixin

// The JSON shapes below mirror the ilink bot HTTP gateway. Item types identify
// the content inside one message; msg_type 1 is a member message and 2 a bot
// message.

const (
	MessageTypeUser = 1
	MessageTypeBot  = 2

	ItemText  = 1
	ItemImage = 2
	ItemVoice = 3
	ItemFile  = 4
	ItemVideo = 5

	MessageStateFinish = 2

	// ErrSessionExpired is returned when the cached context token no longer
	// identifies a live conversation and the member must speak again.
	ErrSessionExpired = -14
	// ErrThrottled is the gateway's burst throttle for proactive sends.
	ErrThrottled = -2
)

type BaseInfo struct {
	ChannelVersion string `json:"channel_version,omitempty"`
}

type UpdatesRequest struct {
	GetUpdatesBuf string   `json:"get_updates_buf"`
	BaseInfo      BaseInfo `json:"base_info"`
}

type Updates struct {
	Ret                int       `json:"ret"`
	Errcode            int       `json:"errcode"`
	Errmsg             string    `json:"errmsg"`
	Msgs               []Message `json:"msgs"`
	GetUpdatesBuf      string    `json:"get_updates_buf"`
	LongPollingTimeout int       `json:"longpolling_timeout_ms"`
}

type TextItem struct {
	Text string `json:"text,omitempty"`
}

type Media struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
}

type ImageItem struct {
	Media     *Media `json:"media,omitempty"`
	Thumb     *Media `json:"thumb_media,omitempty"`
	AESKeyHex string `json:"aeskey,omitempty"`
}

type VoiceItem struct {
	Media      *Media `json:"media,omitempty"`
	Text       string `json:"text,omitempty"`
	EncodeType int    `json:"encode_type,omitempty"`
}

type FileItem struct {
	Media    *Media `json:"media,omitempty"`
	FileName string `json:"file_name,omitempty"`
	Len      string `json:"len,omitempty"`
}

type VideoItem struct {
	Media     *Media `json:"media,omitempty"`
	Thumb     *Media `json:"thumb_media,omitempty"`
	VideoSize int    `json:"video_size,omitempty"`
}

// RefMessage is the quoted message attached to a text item.
type RefMessage struct {
	MessageItem *Item  `json:"message_item,omitempty"`
	Title       string `json:"title,omitempty"`
}

type Item struct {
	Type      int         `json:"type,omitempty"`
	TextItem  *TextItem   `json:"text_item,omitempty"`
	VoiceItem *VoiceItem  `json:"voice_item,omitempty"`
	ImageItem *ImageItem  `json:"image_item,omitempty"`
	FileItem  *FileItem   `json:"file_item,omitempty"`
	VideoItem *VideoItem  `json:"video_item,omitempty"`
	RefMsg    *RefMessage `json:"ref_msg,omitempty"`
}

type Message struct {
	Seq          int64  `json:"seq,omitempty"`
	MessageID    int64  `json:"message_id,omitempty"`
	FromUserID   string `json:"from_user_id,omitempty"`
	ToUserID     string `json:"to_user_id,omitempty"`
	ClientID     string `json:"client_id,omitempty"`
	CreateTimeMS int64  `json:"create_time_ms,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	MessageType  int    `json:"message_type,omitempty"`
	MessageState int    `json:"message_state,omitempty"`
	ItemList     []Item `json:"item_list,omitempty"`
	ContextToken string `json:"context_token,omitempty"`
}

type OutboundMessage struct {
	FromUserID   string `json:"from_user_id"`
	ToUserID     string `json:"to_user_id"`
	ClientID     string `json:"client_id"`
	MessageType  int    `json:"message_type"`
	MessageState int    `json:"message_state"`
	ItemList     []Item `json:"item_list"`
	ContextToken string `json:"context_token"`
}

type SendRequest struct {
	Msg      OutboundMessage `json:"msg"`
	BaseInfo BaseInfo        `json:"base_info"`
}

type SendResponse struct {
	Ret     int    `json:"ret"`
	Errcode int    `json:"errcode"`
	Errmsg  string `json:"errmsg"`
}
