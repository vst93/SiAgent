package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const Repository = "vst93/SiAgent"
const ReleaseBase = "https://github.com/" + Repository + "/releases"

func Mirrors() []string {
	value, ok := os.LookupEnv("SIAGENT_GITHUB_MIRRORS")
	if !ok {
		value = "https://ghproxy.net/,https://gh-proxy.com/"
	}
	result := []string{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		u, err := url.Parse(item)
		if err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" {
			result = append(result, strings.TrimRight(item, "/")+"/")
		}
	}
	return result
}

// Fetch retries the official URL before walking the configured mirror prefixes.
// Each failed attempt truncates its file; HTML/error bodies never become assets.
func Fetch(ctx context.Context, client *http.Client, official, destination string, limit int64, timeout time.Duration, mirrors []string, progress func(string)) error {
	sources := []string{official}
	for _, mirror := range mirrors {
		sources = append(sources, mirror+official)
	}
	for index, source := range sources {
		attempts := 2
		if index == 0 {
			attempts = 3
		}
		for attempt := 1; attempt <= attempts; attempt++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			host := "GitHub"
			if u, err := url.Parse(source); err == nil {
				host = u.Host
			}
			if progress != nil {
				progress(fmt.Sprintf("正在从 %s 下载（尝试 %d/%d）", host, attempt, attempts))
			}
			err := fetchAttempt(ctx, client, source, destination, limit, timeout)
			if err == nil {
				return nil
			}
		}
	}
	os.Remove(destination)
	return fmt.Errorf("GitHub 及配置的镜像下载均失败或超时，请检查网络后重试")
}
func fetchAttempt(ctx context.Context, client *http.Client, source, destination string, limit int64, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", source, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "SiAgent-Updater")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	size, copyErr := io.Copy(file, io.LimitReader(response.Body, limit+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if size > limit {
		return fmt.Errorf("download exceeds limit")
	}
	return nil
}

// ReleaseSize asks the release source for the archive size so the installer can
// report determinate progress. A missing or unknown size is not an error: the
// progress bar simply stays indeterminate.
func ReleaseSize(ctx context.Context, client *http.Client, base, version, asset string, timeout time.Duration) int64 {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := fmt.Sprintf("%s/download/%s/%s", base, version, asset)
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("User-Agent", "SiAgent-Updater")
	response, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0
	}
	if response.ContentLength > 0 {
		return response.ContentLength
	}
	return 0
}
