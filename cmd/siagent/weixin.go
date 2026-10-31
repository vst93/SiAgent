package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"rsc.io/qr"

	protocol "siagent/internal/channelplugins/weixin/protocol"
)

// The personal-WeChat gateway (ilink) has no public callback: a long-lived bot
// token is obtained by scanning a QR code with the phone. This command performs
// that login and prints the values the channel form needs.

const weixinMaxQRRefresh = 3

func runWeixinLogin(ctx context.Context, options commandOptions, output io.Writer) error {
	timeout := 480 * time.Second
	if options.WeixinTimeout > 0 {
		timeout = time.Duration(options.WeixinTimeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	baseURL := strings.TrimSpace(options.WeixinAPI)
	if baseURL == "" {
		baseURL = protocol.DefaultBaseURL
	}
	routeTag := strings.TrimSpace(options.WeixinRouteTag)
	code, err := protocol.FetchQRCode(ctx, baseURL, "", routeTag)
	if err != nil {
		return fmt.Errorf("无法取得微信登录二维码：%w", err)
	}
	refreshes := 0
	scanned := false
	showQR(output, code)
	for {
		if ctx.Err() != nil {
			return fmt.Errorf("等待扫码超时，请重新运行 siagent weixin-login")
		}
		status, err := protocol.PollQRStatus(ctx, baseURL, code.Qrcode, routeTag)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("等待扫码超时，请重新运行 siagent weixin-login")
			}
			return fmt.Errorf("查询扫码状态失败：%w", err)
		}
		switch status.Status {
		case "", "wait":
			// Long-poll timeout without a state change; poll again quietly.
			continue
		case "scaned":
			if !scanned {
				fmt.Fprintln(output, "已扫码，请在手机上确认登录…")
				scanned = true
			}
			continue
		case "expired":
			refreshes++
			if refreshes > weixinMaxQRRefresh {
				return fmt.Errorf("二维码多次过期，请重新运行 siagent weixin-login")
			}
			refreshed, err := protocol.FetchQRCode(ctx, baseURL, "", routeTag)
			if err != nil {
				return fmt.Errorf("刷新二维码失败：%w", err)
			}
			code = refreshed
			scanned = false
			fmt.Fprintf(output, "二维码已过期，已刷新（%d/%d）。\n\n", refreshes, weixinMaxQRRefresh)
			showQR(output, code)
			continue
		case "confirmed":
			if strings.TrimSpace(status.BotToken) == "" || strings.TrimSpace(status.IlinkBotID) == "" {
				return fmt.Errorf("登录已确认，但网关没有返回 bot_token 或 ilink_bot_id")
			}
			fmt.Fprintln(output, "微信登录成功。请在“配置 → 渠道接入 → 新增渠道 → 微信 · 个人号”中填写：")
			fmt.Fprintf(output, "  机器人 ID（bot_id）: %s\n", status.IlinkBotID)
			fmt.Fprintf(output, "  登录令牌（token）: %s\n", status.BotToken)
			if strings.TrimSpace(status.BaseURL) != "" {
				fmt.Fprintf(output, "  网关地址（base_url）: %s\n", status.BaseURL)
			}
			if strings.TrimSpace(status.IlinkUserID) != "" {
				fmt.Fprintf(output, "  扫码用户: %s\n", status.IlinkUserID)
			}
			fmt.Fprintln(output, "\n令牌等同于登录凭据，请勿泄露；失效后重新扫码即可。")
			return nil
		default:
			time.Sleep(time.Second)
		}
	}
}

// showQR prints the login link and a scannable terminal QR code.
func showQR(w io.Writer, code *protocol.QRCode) {
	fmt.Fprintln(w, "请用手机微信扫描二维码，并在手机上确认登录：")
	fmt.Fprintf(w, "URL: %s\n\n", code.QrcodeImgContent)
	printTerminalQR(w, code.QrcodeImgContent)
}

// printTerminalQR renders a scannable QR code with half-width block characters.
// A URL is always printed as well, so terminals with unusual fonts still work.
func printTerminalQR(w io.Writer, content string) {
	code, err := qr.Encode(content, qr.M)
	if err != nil {
		return
	}
	size := code.Size
	const quiet = 4
	blank := strings.Repeat("  ", size+quiet*2)
	for range quiet {
		fmt.Fprintln(w, blank)
	}
	for y := range size {
		var line strings.Builder
		line.WriteString(strings.Repeat("  ", quiet))
		for x := range size {
			if code.Black(x, y) {
				line.WriteString("██")
			} else {
				line.WriteString("  ")
			}
		}
		line.WriteString(strings.Repeat("  ", quiet))
		fmt.Fprintln(w, line.String())
	}
	for range quiet {
		fmt.Fprintln(w, blank)
	}
}
