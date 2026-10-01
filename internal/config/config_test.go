package config
import (
	"os"
	"path/filepath"
	"testing"
	"github.com/stretchr/testify/require"

)

var (
	getenv = os.Getenv
	stat   = os.Stat
)

func TestResolveRootDir(t *testing.T) {
	tmp := t.TempDir()

	origGetenv := getenv
	origStat := stat
	//origOSDefault := osDefaultRoot

	t.Cleanup(func() {
		getenv = origGetenv
		stat = origStat
		//osDefaultRoot = origOSDefault
	})

	t.Run("flag wins", func(t *testing.T) {
		defer func() {
			getenv = origGetenv
		}()

		flagPath := filepath.Join(tmp, "flag")
		require.NoError(t, os.MkdirAll(flagPath, 0755))

		getenv = func(string) string {
			return filepath.Join(tmp, "env")
		}

		root, err := ResolveRootDir(RootDirOptions{
			FlagValue: flagPath,
			EnvKey:    "IEO_EN_CONFIG_ROOT",
			AppName:   "en",
		})

		require.NoError(t, err)
		require.Equal(t, flagPath, root)
	})

	t.Run("env wins", func(t *testing.T) {
		defer func() {
			getenv = origGetenv
		}()

		envPath := filepath.Join(tmp, "env")
		require.NoError(t, os.MkdirAll(envPath, 0755))

		getenv = func(string) string { return envPath }

		root, err := ResolveRootDir(RootDirOptions{
			AppName: "en",
			EnvKey:  "IEO_EN_CONFIG_ROOT",
		})

		require.NoError(t, err)
		require.Equal(t, envPath, root)
	})

	// t.Run("os default used", func(t *testing.T) {
	// 	defer func() {
	// 		osDefaultRoot = origOSDefault
	// 		getenv = origGetenv
	// 	}()

	// 	osDefaultRoot = func(app string) string {
	// 		return filepath.Join(tmp, "osdefault")
	// 	}

	// 	path := osDefaultRoot("en")
	// 	require.NoError(t, os.MkdirAll(path, 0755))

	// 	getenv = func(string) string { return "" }

	// 	root, err := ResolveRootDir(RootDirOptions{
	// 		AppName: "en",
	// 	})

	// 	require.NoError(t, err)
	// 	require.Equal(t, path, root)
	// })

	t.Run("dev fallback bootstrap", func(t *testing.T) {
		defer func() {
			stat = origStat
			getenv = origGetenv
		}()

		getenv = func(string) string { return "" }

		stat = func(string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		}

		root, err := ResolveRootDir(RootDirOptions{
			AppName: "en",
		})

		require.NoError(t, err)
		require.DirExists(t, root)
		require.FileExists(t, filepath.Join(root, "en.yaml"))
	})

	t.Run("creates missing flag dir", func(t *testing.T) {
		p := filepath.Join(tmp, "new")

		root, err := ResolveRootDir(RootDirOptions{
			FlagValue: p,
			AppName:   "en",
		})

		require.NoError(t, err)
		require.DirExists(t, root)
	})
}
