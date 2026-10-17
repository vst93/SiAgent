package platform

import (
	"context"
	"errors"
	"net/http"
	"time"

	"siagent/internal/channels"
)

// CallbackHandler dispatches public callbacks to plugins. Each plugin must
// authenticate its vendor protocol before acknowledging verification or input.
func (s *Server) CallbackHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callbacks/{adapter}/{channel}", s.channelCallback)
	mux.HandleFunc("POST /callbacks/{adapter}/{channel}", s.channelCallback)
	return mux
}

func (s *Server) channelCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	c, secret, err := s.loadChannel(ctx, r.PathValue("channel"))
	if err != nil || c.Archived {
		http.NotFound(w, r)
		return
	}
	provider, _ := s.channelProviders.Get(c.Provider)
	webhook, ok := provider.(channels.WebhookProvider)
	if !ok || webhook.CallbackPath(c.ID) != r.URL.Path {
		http.NotFound(w, r)
		return
	}
	config := channels.Config{ID: c.ID, Provider: c.Provider, Version: c.Version, Values: c.Config, Secrets: secret}
	webhook.ServeCallback(w, r.WithContext(ctx), config,
		func(ctx context.Context, in channels.Incoming) error {
			return s.receiveChannel(ctx, c.ID, c.Version, in)
		},
		func(ctx context.Context) error {
			result, err := s.store.db.ExecContext(ctx, "UPDATE channels SET verified=1 WHERE id=? AND version=? AND archived=0", c.ID, c.Version)
			if err != nil {
				return err
			}
			count, err := result.RowsAffected()
			if err != nil {
				return err
			}
			if count != 1 {
				return errors.New("channel configuration changed")
			}
			return nil
		})
}
