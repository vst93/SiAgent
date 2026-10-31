package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The login flow is the same one the mobile WeChat client uses: fetch a QR code
// for the ilink bot gateway, show it to the operator, then poll its status until
// the phone confirms. The confirmed response carries the long-lived bot token.

const (
	DefaultBotType = "3"
	// qrPollTimeout is the gateway's long-poll window for one status request.
	qrPollTimeout = 35 * time.Second
)

type QRCode struct {
	Qrcode           string `json:"qrcode"`
	QrcodeImgContent string `json:"qrcode_img_content"`
}

type QRStatus struct {
	Status      string `json:"status"`
	BotToken    string `json:"bot_token"`
	IlinkBotID  string `json:"ilink_bot_id"`
	BaseURL     string `json:"baseurl"`
	IlinkUserID string `json:"ilink_user_id"`
}

func loginGet(ctx context.Context, endpoint, routeTag string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	// The gateway expects the mobile client version marker on login calls.
	request.Header.Set("iLink-App-ClientVersion", "1")
	if routeTag != "" {
		request.Header.Set("SKRouteTag", routeTag)
	}
	client := &http.Client{Timeout: qrPollTimeout + 5*time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("weixin login: http %d: %s", response.StatusCode, truncate(raw, 256))
	}
	return raw, nil
}

// FetchQRCode asks the gateway for a fresh login QR code. The returned
// QrcodeImgContent is the URL the operator scans.
func FetchQRCode(ctx context.Context, baseURL, botType, routeTag string) (*QRCode, error) {
	if strings.TrimSpace(botType) == "" {
		botType = DefaultBotType
	}
	endpoint := loginURL(baseURL, "ilink/bot/get_bot_qrcode", map[string][]string{"bot_type": {botType}})
	raw, err := loginGet(ctx, endpoint, routeTag)
	if err != nil {
		return nil, err
	}
	var code QRCode
	if err := json.Unmarshal(raw, &code); err != nil {
		return nil, fmt.Errorf("weixin login: invalid QR response: %w", err)
	}
	if strings.TrimSpace(code.Qrcode) == "" || strings.TrimSpace(code.QrcodeImgContent) == "" {
		return nil, errors.New("weixin login: gateway returned no QR code")
	}
	return &code, nil
}

// PollQRStatus waits for the next login state. A long-poll timeout is reported
// as "wait" so callers can simply poll again.
func PollQRStatus(ctx context.Context, baseURL, qrcode, routeTag string) (*QRStatus, error) {
	endpoint := loginURL(baseURL, "ilink/bot/get_qrcode_status", map[string][]string{"qrcode": {qrcode}})
	raw, err := loginGet(ctx, endpoint, routeTag)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return &QRStatus{Status: "wait"}, nil
		}
		return nil, err
	}
	var status QRStatus
	if err := json.Unmarshal(raw, &status); err != nil {
		return nil, fmt.Errorf("weixin login: invalid status response: %w", err)
	}
	return &status, nil
}
