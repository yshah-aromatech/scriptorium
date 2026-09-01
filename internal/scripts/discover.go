package scripts

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/yshah-aromatech/scriptorium/internal/config"
)

// Script is one discovered script — a folder with a resolved entry file, or
// a loose root .ps1/.py file (Loose).
type Script struct {
	Name           string
	Dir            string
	Entry          string
	Runtime        string // "powershell" | "python"
	Repo           string
	Args           []string
	Description    string
	TimeoutMinutes *float64
	EnvFile        string
	EnvExample     string
	ModuleDir      string
	VenvDir        string
	Loose          bool
}

// skipDirs are repo-root subdirectories discovery never treats as scripts.
var skipDirs = map[string]bool{".git": true, ".github": true, "__pycache__": true, ".venv": true, "node_modules": true}

// candidate is a pre-pass entry — a script.json folder or a loose file —
// before duplicate-name qualification and entry resolution.
type candidate struct {
	repo    Repo
	base    string // original-case name PS counts/qualifies by
	isFile  bool
	dirPath string // set when !isFile
	dirName string // set when !isFile (== base)
	file    string // full path, set when isFile
}

// Discover is the port of Get-StoScripts: one script per repo subfolder
// (with a resolvable entry) plus loose .ps1/.py files at each repo root.
// Duplicate base names across the WHOLE candidate set (case-insensitive)
// qualify as "<repoName>-<base>" in EVERY occurrence — deterministic
// identity that doesn't flip when repo order/contents change, since Name
// keys locks, history, log files and the crontab entry.
func Discover(repos []Repo, paths config.Paths) []Script {
	var candidates []candidate
	nameCount := map[string]int{} // key: strings.ToLower(base) — PS hashtable keys are case-insensitive

	for _, repo := range repos {
		root := repo.Root
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}

		var dirNames, fileNames []string
		for _, e := range entries {
			if e.IsDir() {
				if !skipDirs[strings.ToLower(e.Name())] {
					dirNames = append(dirNames, e.Name())
				}
				continue
			}
			ext := strings.ToLower(filepath.Ext(e.Name()))
			if ext == ".ps1" || ext == ".py" {
				fileNames = append(fileNames, e.Name())
			}
		}
		sortNamesCI(dirNames)
		sortNamesCI(fileNames)

		for _, d := range dirNames {
			candidates = append(candidates, candidate{repo: repo, base: d, dirPath: filepath.Join(root, d), dirName: d})
			nameCount[strings.ToLower(d)]++
		}
		for _, f := range fileNames {
			base := strings.TrimSuffix(f, filepath.Ext(f))
			candidates = append(candidates, candidate{repo: repo, base: base, isFile: true, file: filepath.Join(root, f)})
			nameCount[strings.ToLower(base)]++
		}
	}

	seen := map[string]bool{} // OrdinalIgnoreCase HashSet, keyed lower-case
	var scripts []Script
	for _, c := range candidates {
		name := c.base
		if nameCount[strings.ToLower(c.base)] > 1 {
			name = c.repo.Name + "-" + c.base
		}
		lname := strings.ToLower(name)
		if seen[lname] {
			name += "-2"
			seen[strings.ToLower(name)] = true
		} else {
			seen[lname] = true
		}

		if c.isFile {
			scripts = append(scripts, newScriptInfo(name, c.repo.Root, c.file, nil, c.repo.Name, c.base, paths))
			continue
		}

		meta := loadScriptMeta(filepath.Join(c.dirPath, "script.json"))
		entry := resolveEntry(c.dirPath, c.dirName, meta)
		if entry == "" {
			continue
		}
		scripts = append(scripts, newScriptInfo(name, c.dirPath, entry, meta, c.repo.Name, "", paths))
	}
	return scripts
}

// sortNamesCI matches PS's default Sort-Object Name for these fixtures: a
// case-insensitive lexicographic order.
func sortNamesCI(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
}

// resolveEntry is the port of Resolve-StoEntry: script.json's "entry" wins
// (guarded against escaping the folder via ".."), else the conventional
// PowerShell names, else the conventional Python names, else the sole (or
// first alphabetical) script file of either kind.
func resolveEntry(dirPath, dirName string, meta *scriptMeta) string {
	if meta != nil && meta.Entry != "" {
		p := filepath.Join(dirPath, meta.Entry)
		if _, err := os.Stat(p); err == nil {
			full, ferr := filepath.Abs(p)
			rootFull, rerr := filepath.Abs(dirPath)
			if ferr == nil && rerr == nil {
				full = filepath.Clean(full)
				rootPrefix := filepath.Clean(rootFull) + string(filepath.Separator)
				if strings.HasPrefix(full, rootPrefix) {
					return full
				}
			}
		}
	}

	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sortNamesCI(names)

	var ps1, py []string
	for _, n := range names {
		switch strings.ToLower(filepath.Ext(n)) {
		case ".ps1":
			ps1 = append(ps1, n)
		case ".py":
			py = append(py, n)
		}
	}

	findCI := func(list []string, want string) string {
		for _, n := range list {
			if strings.EqualFold(n, want) {
				return n
			}
		}
		return ""
	}

	for _, c := range []string{"main.ps1", dirName + ".ps1", "run.ps1"} {
		if m := findCI(ps1, c); m != "" {
			return filepath.Join(dirPath, m)
		}
	}
	for _, c := range []string{"main.py", dirName + ".py", "run.py", "__main__.py"} {
		if m := findCI(py, c); m != "" {
			return filepath.Join(dirPath, m)
		}
	}
	if len(ps1) > 0 {
		return filepath.Join(dirPath, ps1[0])
	}
	if len(py) > 0 {
		return filepath.Join(dirPath, py[0])
	}
	return ""
}

// runtimeOf is the port of Get-StoRuntime: extension-based, PowerShell
// unless the entry is a .py file.
func runtimeOf(entry string) string {
	if strings.EqualFold(filepath.Ext(entry), ".py") {
		return "python"
	}
	return "powershell"
}

// scriptMeta is one folder's script.json, decoded field-by-field so a
// malformed individual field (e.g. a non-numeric timeoutMinutes) doesn't
// sink the whole file — matching PS's dynamic PSObject access instead of a
// strict-typed decode.
type scriptMeta struct {
	Entry          string
	Description    string
	Args           []string
	TimeoutMinutes *float64
}

func loadScriptMeta(path string) *scriptMeta {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil {
		return nil
	}
	m := &scriptMeta{}
	if raw, ok := obj["entry"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			m.Entry = s
		}
	}
	if raw, ok := obj["description"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			m.Description = s
		}
	}
	if raw, ok := obj["args"]; ok {
		// ponytail: only a plain JSON string array is supported — PS
		// stringifies any element type via "$_"; upgrade to per-element
		// coercion if a repo ever ships non-string script.json args.
		var arr []string
		if json.Unmarshal(raw, &arr) == nil {
			m.Args = arr
		}
	}
	if raw, ok := obj["timeoutMinutes"]; ok {
		if v, ok2 := numericCast(raw); ok2 {
			m.TimeoutMinutes = &v
		}
	}
	return m
}

// numericCast mirrors PS's `-as [double]`: a JSON number or a numeric
// string casts; anything else (including non-finite) does not.
func numericCast(raw json.RawMessage) (float64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return 0, false
	}
	var f float64
	switch v := tok.(type) {
	case json.Number:
		f, err = v.Float64()
	case string:
		f, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
	default:
		return 0, false
	}
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// newScriptInfo is the port of New-StoScriptInfo.
func newScriptInfo(name, dir, entry string, meta *scriptMeta, repoName, envBase string, paths config.Paths) Script {
	var args []string
	var desc string
	var timeout *float64
	if meta != nil {
		args = meta.Args
		desc = meta.Description
		timeout = meta.TimeoutMinutes
	}
	envFileName, envExampleName := ".env", ".env.example"
	if envBase != "" {
		envFileName, envExampleName = envBase+".env", envBase+".env.example"
	}
	return Script{
		Name:           name,
		Dir:            dir,
		Entry:          entry,
		Runtime:        runtimeOf(entry),
		Repo:           repoName,
		Args:           args,
		Description:    desc,
		TimeoutMinutes: timeout,
		EnvFile:        filepath.Join(dir, envFileName),
		EnvExample:     filepath.Join(dir, envExampleName),
		ModuleDir:      filepath.Join(paths.ModulesDir, name),
		VenvDir:        filepath.Join(paths.VenvsDir, name),
		Loose:          envBase != "",
	}
}
