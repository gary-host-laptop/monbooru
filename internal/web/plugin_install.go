package web

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/monbooru/monbooru/internal/config"
	"github.com/monbooru/monbooru/internal/logx"
)

// pluginManifest is the launch declaration a dropped plugin folder carries in
// its plugin.toml. The folder name is the plugin name; the manifest only says
// how to run what is inside it.
type pluginManifest struct {
	Command        string   `toml:"command"`
	Args           []string `toml:"args"`
	CommandWindows string   `toml:"command_windows"`
	ArgsWindows    []string `toml:"args_windows"`
}

// launchFor picks the per-OS launch line: the windows overrides on Windows
// when present, the plain keys everywhere else.
func (m pluginManifest) launchFor(goos string) (string, []string) {
	if goos == "windows" && m.CommandWindows != "" {
		if m.ArgsWindows != nil {
			return m.CommandWindows, m.ArgsWindows
		}
		return m.CommandWindows, m.Args
	}
	return m.Command, m.Args
}

// installedPlugin is one discovered folder: the effective launch line plus
// where it lives, which becomes the process's working directory.
type installedPlugin struct {
	Name    string
	Command string
	Args    []string
	Dir     string
}

// effectivePlugin is a config block joined with the folder that supplies its
// launch line, when one does. Installed marks the folder form, whose run
// state the operator toggles from the web and which therefore persists as
// Enabled on the block. The launch line stays off PluginConfig: it comes
// from the folder's manifest and is never written to monbooru.toml, so a
// web-writable exec line cannot exist.
type effectivePlugin struct {
	config.PluginConfig
	Command   string
	Args      []string
	Dir       string
	Installed bool
}

// pluginsDir is where dropped plugin folders live, next to monbooru.toml.
// Absolute, because a folder-relative command becomes the launched process's
// path while its folder becomes that process's working directory: a relative
// path would then resolve against the folder instead of monbooru's cwd.
func (s *Server) pluginsDir() string { return s.configSubdir("plugins") }

// configSubdir is one of the folders monbooru keeps next to monbooru.toml,
// as an absolute path. The settings hints print these, and a `-config` given
// as a relative path would otherwise print one only the process's own working
// directory can resolve.
func (s *Server) configSubdir(name string) string {
	if s.configPath == "" {
		return ""
	}
	dir := filepath.Join(filepath.Dir(s.configPath), name)
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	return abs
}

// ensurePluginsDir creates the plugins folder at boot so there is somewhere
// obvious to drop one. Unlike themes it seeds nothing: an example here would
// be executable code.
func (s *Server) ensurePluginsDir() {
	dir := s.pluginsDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logx.Warnf("plugins: could not create %s: %v", dir, err)
	}
}

// discoverInstalled scans the plugins folder for subfolders carrying a
// plugin.toml. Nothing runs on discovery; the scan only feeds the settings
// rows and the boot-start pass, which both gate on the operator's choice.
func (s *Server) discoverInstalled() []installedPlugin {
	dir := s.pluginsDir()
	if dir == "" {
		return nil
	}
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []installedPlugin
	for _, it := range items {
		if !it.IsDir() {
			continue
		}
		name := it.Name()
		folder := filepath.Join(dir, name)
		var m pluginManifest
		if _, err := toml.DecodeFile(filepath.Join(folder, "plugin.toml"), &m); err != nil {
			if !os.IsNotExist(err) {
				logx.Warnf("plugins: skipping %s: %v", folder, err)
			}
			continue
		}
		// The companion keeps its own config section and surfaces; a folder
		// borrowing its name would collide with them.
		if name == monloaderApp {
			logx.Warnf("plugins: skipping %s: %q is the companion's name", folder, name)
			continue
		}
		if err := config.ValidatePluginName(name); err != nil {
			logx.Warnf("plugins: skipping %s: %v", folder, err)
			continue
		}
		command, args := m.launchFor(runtime.GOOS)
		if command == "" {
			logx.Warnf("plugins: skipping %s: plugin.toml names no command", folder)
			continue
		}
		out = append(out, installedPlugin{
			Name:    name,
			Command: resolvePluginCommand(folder, command),
			Args:    args,
			Dir:     folder,
		})
	}
	return out
}

// resolvePluginCommand anchors a folder-relative launch line to its folder. A
// bare name stays a PATH lookup; an absolute path is used as written.
func resolvePluginCommand(dir, command string) string {
	if filepath.IsAbs(command) || !strings.ContainsAny(command, `/\`) {
		return command
	}
	return filepath.Join(dir, command)
}

// effectivePlugins merges the configured blocks with the discovered folders
// by name: the block carries the pairing halves and the operator's flags, the
// manifest the launch line.
func (s *Server) effectivePlugins() []effectivePlugin {
	blocks := s.plugins()
	out := make([]effectivePlugin, 0, len(blocks))
	byName := make(map[string]int, len(blocks))
	for _, p := range blocks {
		byName[p.Name] = len(out)
		out = append(out, effectivePlugin{PluginConfig: p})
	}
	for _, ins := range s.discoverInstalled() {
		i, ok := byName[ins.Name]
		if !ok {
			out = append(out, effectivePlugin{
				PluginConfig: config.PluginConfig{Name: ins.Name},
				Command:      ins.Command,
				Args:         ins.Args,
				Dir:          ins.Dir,
				Installed:    true,
			})
			continue
		}
		out[i].Command, out[i].Args = ins.Command, ins.Args
		out[i].Dir, out[i].Installed = ins.Dir, true
	}
	return out
}

// effective returns the merged view of one plugin, or false.
func (s *Server) effective(name string) (effectivePlugin, bool) {
	for _, p := range s.effectivePlugins() {
		if p.Name == name {
			return p, true
		}
	}
	return effectivePlugin{}, false
}
