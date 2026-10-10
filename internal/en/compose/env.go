package compose

import "strings"

// cliEnvAllowlist is every variable the Compose CLI gets from the EN's environment: what it needs
// to reach the engine, and what the OS needs to run it (SPEC §15.6, ADR 0017). Anything else,
// such as an en.* setting, a NATS password or a proxy, never reaches the CLI, so `${…}` in an
// archive's compose.yaml cannot read it into a workload (SPEC §9.2).
var cliEnvAllowlist = []string{
	// engine
	"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "DOCKER_CERT_PATH", "DOCKER_TLS_VERIFY",
	"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINERS_CONF", "XDG_RUNTIME_DIR",
	// OS
	"PATH", "HOME", "TMPDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramData",
	"ProgramFiles", "SystemRoot", "SystemDrive", "windir", "PATHEXT", "ComSpec", "TEMP", "TMP",
}

// cliEnv returns the entries of environ, in `name=value` form, whose names are on the allowlist.
// Names compare without regard to case, as Windows does.
func cliEnv(environ []string) []string {
	env := []string{}
	for _, kv := range environ {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" { // Windows keeps per-drive directories as "=C:=C:\…"
			continue
		}
		for _, allowed := range cliEnvAllowlist {
			if strings.EqualFold(name, allowed) {
				env = append(env, kv)
				break
			}
		}
	}
	return env
}
