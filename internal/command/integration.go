package command

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func probeGrobid(ctx context.Context, base string) error {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return errors.New("invalid GROBID URL")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/isalive"
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("GROBID is not ready")
	}
	return nil
}
