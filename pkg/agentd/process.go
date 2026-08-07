package agentd

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	toolUIDEnv  = "AL_AGENTD_TOOL_UID"
	toolGIDEnv  = "AL_AGENTD_TOOL_GID"
	toolHomeEnv = "AL_AGENTD_TOOL_HOME"
	toolUserEnv = "AL_AGENTD_TOOL_USER"
	mcpUIDEnv   = "AL_AGENTD_MCP_UID"
	mcpGIDEnv   = "AL_AGENTD_MCP_GID"
	mcpHomeEnv  = "AL_AGENTD_MCP_HOME"
	mcpUserEnv  = "AL_AGENTD_MCP_USER"
)

type processIdentity struct {
	uidEnv               string
	gidEnv               string
	homeEnv              string
	userEnv              string
	supplementaryGIDEnvs []string
}

var (
	toolIdentity = processIdentity{uidEnv: toolUIDEnv, gidEnv: toolGIDEnv, homeEnv: toolHomeEnv, userEnv: toolUserEnv}
	mcpIdentity  = processIdentity{
		uidEnv:               mcpUIDEnv,
		gidEnv:               mcpGIDEnv,
		homeEnv:              mcpHomeEnv,
		userEnv:              mcpUserEnv,
		supplementaryGIDEnvs: []string{toolGIDEnv},
	}
)

var inheritedChildEnv = map[string]struct{}{
	"AGENT_BROWSER_EXECUTABLE_PATH": {},
	"COLORTERM":                     {},
	"GIT_SSL_CAINFO":                {},
	"HOME":                          {},
	"HTTP_PROXY":                    {},
	"HTTPS_PROXY":                   {},
	"LANG":                          {},
	"LANGUAGE":                      {},
	"LC_ALL":                        {},
	"LOGNAME":                       {},
	"NO_PROXY":                      {},
	"NODE_EXTRA_CA_CERTS":           {},
	"PATH":                          {},
	"SHELL":                         {},
	"SSL_CERT_DIR":                  {},
	"SSL_CERT_FILE":                 {},
	"TERM":                          {},
	"TMPDIR":                        {},
	"TZ":                            {},
	"USER":                          {},
	"http_proxy":                    {},
	"https_proxy":                   {},
	"no_proxy":                      {},
}

func childProcessEnv(overrides map[string]string) ([]string, error) {
	return processEnv(toolIdentity, overrides)
}

func processEnv(identity processIdentity, overrides map[string]string) ([]string, error) {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, allowed := inheritedChildEnv[key]; allowed || strings.HasPrefix(key, "LC_") {
			values[key] = value
		}
	}
	if value := strings.TrimSpace(os.Getenv(identity.homeEnv)); value != "" {
		values["HOME"] = value
	}
	if value := strings.TrimSpace(os.Getenv(identity.userEnv)); value != "" {
		values["USER"] = value
		values["LOGNAME"] = value
	}
	for key, value := range overrides {
		if key == "" || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("invalid child environment variable %q", key)
		}
		if reservedPlatformEnv(key) {
			return nil, fmt.Errorf("child environment variable %q is reserved", key)
		}
		values[key] = value
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result, nil
}

func childProcessSysProcAttr(setProcessGroup bool) (*syscall.SysProcAttr, error) {
	return processSysProcAttr(toolIdentity, setProcessGroup)
}

func processSysProcAttr(identity processIdentity, setProcessGroup bool) (*syscall.SysProcAttr, error) {
	attr := &syscall.SysProcAttr{Setpgid: setProcessGroup}
	uid, gid, configured, err := configuredProcessIdentity(identity)
	if err != nil || !configured {
		return attr, err
	}
	if os.Geteuid() != 0 {
		if os.Geteuid() == int(uid) && os.Getegid() == int(gid) {
			return attr, nil
		}
		return nil, fmt.Errorf("agentd must run as root to start tools with uid %d", uid)
	}
	attr.Credential = &syscall.Credential{
		Uid:    uid,
		Gid:    gid,
		Groups: processSupplementaryGroups(identity, gid),
	}
	return attr, nil
}

func processSupplementaryGroups(identity processIdentity, primaryGID uint32) []uint32 {
	groups := []uint32{primaryGID}
	for _, envName := range identity.supplementaryGIDEnvs {
		value, err := strconv.ParseUint(strings.TrimSpace(os.Getenv(envName)), 10, 32)
		if err != nil || value == 0 {
			continue
		}
		gid := uint32(value)
		if gid != primaryGID {
			groups = append(groups, gid)
		}
	}
	return groups
}

func configuredToolIdentity() (uint32, uint32, bool, error) {
	return configuredProcessIdentity(toolIdentity)
}

func configuredProcessIdentity(identity processIdentity) (uint32, uint32, bool, error) {
	rawUID := strings.TrimSpace(os.Getenv(identity.uidEnv))
	rawGID := strings.TrimSpace(os.Getenv(identity.gidEnv))
	if rawUID == "" && rawGID == "" {
		return 0, 0, false, nil
	}
	if rawUID == "" || rawGID == "" {
		return 0, 0, false, fmt.Errorf("%s and %s must be configured together", identity.uidEnv, identity.gidEnv)
	}
	uid, err := strconv.ParseUint(rawUID, 10, 32)
	if err != nil || uid == 0 {
		return 0, 0, false, fmt.Errorf("%s must be a positive 32-bit integer", identity.uidEnv)
	}
	gid, err := strconv.ParseUint(rawGID, 10, 32)
	if err != nil || gid == 0 {
		return 0, 0, false, fmt.Errorf("%s must be a positive 32-bit integer", identity.gidEnv)
	}
	return uint32(uid), uint32(gid), true, nil
}

func reservedPlatformEnv(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	return strings.HasPrefix(upper, "AL_") || strings.HasPrefix(upper, "KUBERNETES_SERVICE_")
}

func expandMCPConfigEnv(value string) string {
	return os.Expand(value, func(name string) string {
		if reservedPlatformEnv(name) {
			return ""
		}
		return os.Getenv(name)
	})
}
