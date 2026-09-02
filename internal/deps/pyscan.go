package deps

import (
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed scanner.py
var scannerPy []byte

// pipNameMap is $script:PipNameMap from src/Deps.psm1, verbatim — the
// common "imported name" -> "gallery/pip name" mismatches. Lookups are
// case-insensitive (PS hashtable literals are); the returned value keeps
// its map-declared casing regardless of the input's casing.
var pipNameMap = map[string]string{
	"cv2":         "opencv-python",
	"PIL":         "pillow",
	"sklearn":     "scikit-learn",
	"skimage":     "scikit-image",
	"bs4":         "beautifulsoup4",
	"yaml":        "pyyaml",
	"dotenv":      "python-dotenv",
	"dateutil":    "python-dateutil",
	"Crypto":      "pycryptodome",
	"nacl":        "pynacl",
	"serial":      "pyserial",
	"usb":         "pyusb",
	"psycopg2":    "psycopg2-binary",
	"MySQLdb":     "mysqlclient",
	"git":         "GitPython",
	"github":      "PyGithub",
	"jwt":         "PyJWT",
	"docx":        "python-docx",
	"pptx":        "python-pptx",
	"fitz":        "PyMuPDF",
	"magic":       "python-magic",
	"websocket":   "websocket-client",
	"websockets":  "websockets",
	"telegram":    "python-telegram-bot",
	"kafka":       "kafka-python",
	"zmq":         "pyzmq",
	"OpenSSL":     "pyopenssl",
	"Levenshtein": "python-Levenshtein",
	"gi":          "PyGObject",
	"cairo":       "pycairo",
	"win32api":    "pywin32",
	"attr":        "attrs",
	"google":      "google-api-python-client",
}

// pipNameMapLower is pipNameMap keyed by lower-case for the case-insensitive
// lookup PS hashtable literals give for free.
var pipNameMapLower = func() map[string]string {
	m := make(map[string]string, len(pipNameMap))
	for k, v := range pipNameMap {
		m[strings.ToLower(k)] = v
	}
	return m
}()

// PipName is the port of Get-StoPipName: the pip package name to install for
// an imported module name, or the name itself when unmapped.
func PipName(module string) string {
	if v, ok := pipNameMapLower[strings.ToLower(module)]; ok {
		return v
	}
	return module
}

// VenvPython is the port of Get-StoVenvPython: POSIX-only, same stance as
// the rest of the app.
func VenvPython(venvDir string) string {
	return filepath.Join(venvDir, "bin", "python")
}

// HasVenv is the port of Test-StoVenv.
func HasVenv(venvDir string) bool {
	_, err := os.Stat(VenvPython(venvDir))
	return err == nil
}

// pyScanDoc is scanner.py's JSON output shape.
type pyScanDoc struct {
	Missing   []string `json:"missing"`
	Installed []string `json:"installed"`
}

// ScanPython is the port of Get-StoMissingPythonDeps: §9.5.
//
// requirements.txt in dir takes precedence over the AST scan entirely: its
// names (verbatim, no pip-name mapping — they're already real package
// names) are checked against `pip list --format=json` inside the venv
// (underscore/hyphen normalized both sides, case-insensitive); no venv means
// everything listed is missing.
//
// Otherwise the embedded AST scanner runs via the venv's python (or the
// system pythonBin when there's no venv yet — just to find the imports; its
// own installed/missing split is ignored in that case since nothing is
// actually installed anywhere). A missing interpreter (neither on PATH nor
// present as a literal file) yields an empty result, not an error — a
// zero-import script never needs a venv at all.
func (s *Scanner) ScanPython(dir, venvDir, pythonBin string) ([]Dep, error) {
	hasVenv := HasVenv(venvDir)

	if reqPath := requirementsPath(dir); reqPath != "" {
		wanted := ReadRequirements(reqPath)
		if len(wanted) == 0 {
			return nil, nil
		}
		have := map[string]bool{}
		if hasVenv {
			for _, name := range installedPipNames(VenvPython(venvDir)) {
				have[normalizePipName(name)] = true
			}
		}
		var missing []Dep
		for _, name := range wanted {
			if !have[normalizePipName(name)] {
				missing = append(missing, Dep{Name: name, Display: name, PipName: name})
			}
		}
		return missing, nil
	}

	py := pythonBin
	if hasVenv {
		py = VenvPython(venvDir)
	}
	if !interpreterAvailable(py) {
		return nil, nil
	}

	out, err := exec.Command(py, "-c", string(scannerPy), dir).CombinedOutput()
	if err != nil {
		return nil, nil // PS parity: a scan failure yields an empty result, not an error
	}
	line := lastNonEmptyLine(out)
	var scan pyScanDoc
	if line == "" || json.Unmarshal([]byte(line), &scan) != nil {
		return nil, nil
	}

	names := append([]string{}, scan.Missing...)
	if !hasVenv {
		names = append(names, scan.Installed...)
	}
	sortNamesCI(names)

	deps := make([]Dep, len(names))
	for i, name := range names {
		pip := PipName(name)
		display := name
		if pip != name {
			display = name + " (pip: " + pip + ")"
		}
		deps[i] = Dep{Name: name, Display: display, PipName: pip}
	}
	return deps, nil
}

// requirementsPath is the port of Get-StoRequirementsFile.
func requirementsPath(dir string) string {
	p := filepath.Join(dir, "requirements.txt")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// normalizePipName is the underscore<->hyphen fold both requirements.txt
// names and `pip list` names go through before comparison, case-insensitive.
func normalizePipName(name string) string {
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

// installedPipNames runs `python -m pip list --format=json` inside a venv
// and returns the raw package names; any failure (missing pip, bad JSON)
// yields an empty list, matching PS's try/catch-swallow.
func installedPipNames(venvPython string) []string {
	out, err := exec.Command(venvPython, "-m", "pip", "list", "--format=json").Output()
	if err != nil {
		return nil
	}
	var pkgs []struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(out, &pkgs) != nil {
		return nil
	}
	names := make([]string, len(pkgs))
	for i, p := range pkgs {
		names[i] = p.Name
	}
	return names
}

// sortNamesCI is Deps.psm1:454's `Sort-Object` (default: case-insensitive)
// on the AST scanner's third-party import names — sort.Strings is ordinal
// and would put every uppercase name first (e.g. "Crypto" before "attr"),
// diverging from PS in both the printed "installing missing modules: ..."
// line and the generated `pip install @(...)` package list. Same
// case-insensitive-with-lowercase-first-tiebreak comparator as
// internal/scripts' sortNamesCI (PS is culture-aware rather than strictly
// ordinal-case-insensitive; for plain package names the two coincide, the
// same accepted gap as registry entry 19).
func sortNamesCI(names []string) {
	sort.SliceStable(names, func(i, j int) bool {
		li, lj := strings.ToLower(names[i]), strings.ToLower(names[j])
		if li != lj {
			return li < lj
		}
		return names[i] > names[j]
	})
}

// interpreterAvailable mirrors `Get-Command $py -ErrorAction SilentlyContinue`
// (resolves via PATH, or is itself an existing file) `-or (Test-Path $py)`.
func interpreterAvailable(py string) bool {
	if _, err := exec.LookPath(py); err == nil {
		return true
	}
	_, err := os.Stat(py)
	return err == nil
}
