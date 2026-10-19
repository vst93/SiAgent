package platform

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// advancedToolsDisabled reports the deployment-level kill switch. Any of the
// usual truthy values disables read/write/edit/bash for every Agent.
func advancedToolsDisabled() bool { return advancedToolsDisabledConfig(nil) }

func advancedToolsDisabledConfig(options []ServerOptions) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv("SIAGENT_DISABLE_ADVANCED_TOOLS")))
	if value != "" {
		if value == "0" || value == "false" || value == "no" {
			return false
		}
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
		return true
	}
	if len(options) > 0 && options[0].AdvancedToolsDisabled != nil {
		return *options[0].AdvancedToolsDisabled
	}
	return false
}

// audit appends one administrator action. Detail must never contain message
// bodies, tool arguments or credentials.
func (s *Server) audit(action, resource, detail string) {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	_, _ = s.store.db.Exec("INSERT INTO audit_log(actor,action,resource,detail,created) VALUES('admin',?,?,?,?)", action, resource, detail, time.Now().UnixMilli())
}
