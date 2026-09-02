import ast, json, os, sys, importlib.util

script_dir = sys.argv[1]
stdlib = set(getattr(sys, "stdlib_module_names", set())) | {"__future__"}

local, imports = set(), set()
for root, dirs, files in os.walk(script_dir):
    dirs[:] = [d for d in dirs if not d.startswith((".", "__"))]
    for d in dirs:
        local.add(d)
    for f in files:
        if f.endswith(".py"):
            local.add(os.path.splitext(f)[0])

for root, dirs, files in os.walk(script_dir):
    dirs[:] = [d for d in dirs if not d.startswith((".", "__"))]
    for f in files:
        if not f.endswith(".py"):
            continue
        try:
            with open(os.path.join(root, f), encoding="utf-8", errors="replace") as fh:
                tree = ast.parse(fh.read())
        except SyntaxError:
            continue
        for node in ast.walk(tree):
            if isinstance(node, ast.Import):
                for a in node.names:
                    imports.add(a.name.split(".")[0])
            elif isinstance(node, ast.ImportFrom) and node.level == 0 and node.module:
                imports.add(node.module.split(".")[0])

third_party = sorted(m for m in imports if m and m not in stdlib and m not in local)
missing, installed = [], []
for m in third_party:
    try:
        spec = importlib.util.find_spec(m)
    except (ImportError, ValueError, ModuleNotFoundError):
        spec = None
    (installed if spec else missing).append(m)

print(json.dumps({"missing": missing, "installed": installed}))
