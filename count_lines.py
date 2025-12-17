import os

def count_lines(filepath):
    """
    Counts lines in a file. Returns 0 if reading fails.
    """
    try:
        with open(filepath, 'r', encoding='utf-8', errors='ignore') as f:
            return sum(1 for _ in f)
    except Exception as e:
        print(f"Error reading {filepath}: {e}")
        return 0

def main():
    # Initialize counters
    categories = {
        "Go": 0,
        "Go Test": 0,
        "C++": 0,
        "Templates": 0,
        "Flatbuffers": 0,
        "Build/Config": 0,
        "Documentation": 0,
        "Scripts": 0,
        "Other": 0
    }

    file_counts = {k: 0 for k in categories}
    other_files = []

    # Configuration for classification
    config_files = {'cmakelists.txt', 'taskfile.yml', 'go.mod', 'go.sum', 'xll.yaml', '.gitignore'}

    # Binary/Asset extensions to ignore
    ignored_exts = {
        '.png', '.jpg', '.jpeg', '.gif', '.ico',
        '.exe', '.dll', '.so', '.a', '.lib', '.o', '.obj',
        '.zip', '.tar', '.gz', '.7z',
        '.xlsx', '.xls', '.docx', '.pdf',
        '.csv'
    }

    print("Counting lines of code (excluding Flatbuffers generated files)...")

    for root, dirs, files in os.walk("."):
        # Skip hidden directories
        if ".git" in dirs:
            dirs.remove(".git")
        if ".jules" in dirs:
            dirs.remove(".jules")

        for file in files:
            filepath = os.path.join(root, file)

            # Skip this script itself
            if file == "count_lines.py":
                continue

            # Normalize path for consistent checking
            norm_path = os.path.normpath(filepath).replace("\\", "/")

            # --- Exclusion Logic for Generated Code ---
            is_excluded = False

            # 1. Exclude generated C++ header for protocol
            if norm_path.endswith("internal/assets/files/protocol_generated.h"):
                is_excluded = True

            # 2. Exclude generated Go package for protocol (except .fbs source)
            if "pkg/protocol" in norm_path and file.endswith(".go"):
                is_excluded = True

            if is_excluded:
                continue
            # ------------------------------------------

            ext = os.path.splitext(file)[1].lower()
            filename = file.lower()

            if ext in ignored_exts:
                continue

            lines = count_lines(filepath)

            # Classification Logic
            category = "Other"
            if filename.endswith("_test.go"):
                category = "Go Test"
            elif ext == ".go":
                category = "Go"
            elif ext in [".cpp", ".h", ".hpp", ".c"]:
                category = "C++"
            elif ext == ".tmpl":
                category = "Templates"
            elif ext == ".fbs":
                category = "Flatbuffers"
            elif filename in config_files or ext in ['.yaml', '.yml', '.mod', '.sum']:
                 category = "Build/Config"
            elif ext in [".md", ".txt"] or "license" in filename:
                category = "Documentation"
            elif ext in [".sh", ".bat", ".ps1", ".py"]:
                category = "Scripts"

            categories[category] += lines
            file_counts[category] += 1

            if category == "Other":
                other_files.append((filepath, lines))

    # Output Results
    print(f"\n{'Category':<20} | {'Files':<10} | {'Lines':<10}")
    print("-" * 46)

    total_files = 0
    total_lines = 0

    # Sort by line count descending
    sorted_cats = sorted(categories.items(), key=lambda x: x[1], reverse=True)

    for cat, lines in sorted_cats:
        files = file_counts[cat]
        print(f"{cat:<20} | {files:<10} | {lines:<10}")
        total_files += files
        total_lines += lines

    print("-" * 46)
    print(f"{'TOTAL':<20} | {total_files:<10} | {total_lines:<10}")

    if other_files:
        print("\nTop 'Other' files:")
        other_files.sort(key=lambda x: x[1], reverse=True)
        for fp, l in other_files[:10]:
            print(f"{l:<6} {fp}")

if __name__ == "__main__":
    main()
