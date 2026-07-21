package arch

import (
	"bufio"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Role is the architectural role assigned to a package by its repository path.
type Role string

const (
	RoleUnclassified      Role = "unclassified"
	RoleKernel            Role = "kernel"
	RoleActivationRecords Role = "activationrecords"
	RoleApps              Role = "apps"
	RoleProviderInterface Role = "provider interface"
	RoleInfraAdapter      Role = "infra adapter"
	RoleModule            Role = "module"
	RoleModuleDefinition  Role = "module definition"
	RoleBuiltin           Role = "builtin"
)

const (
	RuleSiblingModule      = "modules may not import a sibling module"
	RuleDomainAdapters     = "domain modules may not import adapters"
	RuleDomainRawExec      = "domain modules may not import os/exec"
	RuleActivationInternal = "only activationrecords may import activationrecords/internal"
	RuleModuleInternal     = "only a module may import its own modules/<name>/internal"
	RuleBuiltinDefinitions = "builtin may import only module definition packages"
	RuleKernelImports      = "kernel may import only stdlib and kernel packages"
)

// Violation is one forbidden import edge. File and package paths are relative
// to the analyzed root, using slash separators.
type Violation struct {
	File     string
	Importer string
	Imported string
	Rule     string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s: %s imports %s: %s", v.File, v.Importer, v.Imported, v.Rule)
}

type parsedFile struct {
	file        string
	packagePath string
	imports     []string
}

// Analyze walks root and returns every import-graph violation it finds.
// It deliberately uses only parsing: analyzed fixture trees do not need to be
// complete or compilable Go modules.
func Analyze(root string) ([]Violation, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	modulePath, err := readModulePath(root)
	if err != nil {
		return nil, err
	}

	var files []parsedFile
	localPackages := make(map[string]bool)
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(filePath) != ".go" {
			return nil
		}

		relFile, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		relFile = filepath.ToSlash(relFile)
		packagePath := path.Dir(relFile)
		if packagePath == "." {
			packagePath = ""
		}
		localPackages[packagePath] = true

		file, err := parser.ParseFile(fset, filePath, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("parse %s: %w", relFile, err)
		}
		imports := make([]string, 0, len(file.Imports))
		for _, spec := range file.Imports {
			imports = append(imports, strings.Trim(spec.Path.Value, `"`))
		}
		files = append(files, parsedFile{file: relFile, packagePath: packagePath, imports: imports})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool { return files[i].file < files[j].file })
	var violations []Violation
	for _, file := range files {
		for _, imported := range file.imports {
			importedRepoPath := repoPathForImport(imported, modulePath, localPackages)
			violations = append(violations, checkImport(file, imported, importedRepoPath)...)
		}
	}
	sort.SliceStable(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		if violations[i].Imported != violations[j].Imported {
			return violations[i].Imported < violations[j].Imported
		}
		return violations[i].Rule < violations[j].Rule
	})
	return violations, nil
}

// ClassifyPackage assigns a role to a repository-relative package path.
func ClassifyPackage(packagePath string) Role {
	parts := splitRepoPath(packagePath)
	if len(parts) == 0 {
		return RoleUnclassified
	}
	switch parts[0] {
	case "kernel":
		if len(parts) >= 2 && parts[1] == "provider" {
			return RoleProviderInterface
		}
		return RoleKernel
	case "activationrecords":
		return RoleActivationRecords
	case "builtin":
		return RoleBuiltin
	case "adapters":
		if len(parts) >= 2 {
			switch parts[1] {
			case "build", "podman", "caddy", "host":
				return RoleInfraAdapter
			}
		}
	case "modules":
		if len(parts) >= 2 && parts[1] != "" {
			if parts[1] == "apps" {
				return RoleApps
			}
			if len(parts) == 3 && parts[2] == "definition" {
				return RoleModuleDefinition
			}
			return RoleModule
		}
	}
	return RoleUnclassified
}

func checkImport(file parsedFile, imported, importedRepoPath string) []Violation {
	importer := file.packagePath
	target := importedRepoPath
	var violations []Violation

	// Rules concerning modules apply to all packages below modules/<name>,
	// including that module's definition and internal packages.
	if moduleName, ok := moduleNameForPath(importer); ok {
		if importedModule, ok := moduleNameForPath(target); ok && importedModule != moduleName {
			violations = append(violations, violation(file, imported, RuleSiblingModule))
		}
		if strings.HasPrefix(target, "adapters/") {
			violations = append(violations, violation(file, imported, RuleDomainAdapters))
		}
		if imported == "os/exec" {
			violations = append(violations, violation(file, imported, RuleDomainRawExec))
		}
	}

	if isUnder(target, "activationrecords/internal") && !isUnder(importer, "activationrecords") {
		violations = append(violations, violation(file, imported, RuleActivationInternal))
	}
	if importedModule, ok := internalModuleForPath(target); ok {
		if importerModule, ok := moduleNameForPath(importer); !ok || importerModule != importedModule {
			violations = append(violations, violation(file, imported, RuleModuleInternal))
		}
	}

	if isUnder(importer, "builtin") && !isStdlibImport(imported, target) {
		if !isModuleDefinitionPath(target) {
			violations = append(violations, violation(file, imported, RuleBuiltinDefinitions))
		}
	}

	if isUnder(importer, "kernel") && !isStdlibImport(imported, target) && !isKernelPath(target) {
		violations = append(violations, violation(file, imported, RuleKernelImports))
	}

	return violations
}

func violation(file parsedFile, imported, rule string) Violation {
	return Violation{File: file.file, Importer: file.packagePath, Imported: imported, Rule: rule}
}

func moduleNameForPath(packagePath string) (string, bool) {
	parts := splitRepoPath(packagePath)
	if len(parts) >= 2 && parts[0] == "modules" && parts[1] != "" {
		return parts[1], true
	}
	return "", false
}

func internalModuleForPath(packagePath string) (string, bool) {
	parts := splitRepoPath(packagePath)
	if len(parts) >= 3 && parts[0] == "modules" && parts[2] == "internal" {
		return parts[1], true
	}
	return "", false
}

func isModuleDefinitionPath(packagePath string) bool {
	parts := splitRepoPath(packagePath)
	return len(parts) == 3 && parts[0] == "modules" && parts[1] != "" && parts[2] == "definition"
}

func isKernelPath(packagePath string) bool {
	parts := splitRepoPath(packagePath)
	return len(parts) >= 1 && parts[0] == "kernel"
}

func isUnder(packagePath, directory string) bool {
	return packagePath == directory || strings.HasPrefix(packagePath, directory+"/")
}

func isStdlibImport(imported, repoPath string) bool {
	if repoPath != "" {
		return false
	}
	first := strings.Split(imported, "/")[0]
	return first != "" && !strings.Contains(first, ".")
}

func repoPathForImport(imported, modulePath string, localPackages map[string]bool) string {
	if modulePath != "" {
		if imported == modulePath {
			return ""
		}
		prefix := modulePath + "/"
		if strings.HasPrefix(imported, prefix) {
			return strings.TrimPrefix(imported, prefix)
		}
	}
	if localPackages[imported] {
		return imported
	}
	if isArchitecturalPath(imported) {
		return imported
	}
	for localPath := range localPackages {
		if localPath != "" && strings.HasPrefix(imported, localPath+"/") {
			return imported
		}
	}
	return ""
}

func isArchitecturalPath(imported string) bool {
	first := strings.Split(imported, "/")[0]
	switch first {
	case "kernel", "activationrecords", "adapters", "modules", "builtin":
		return true
	default:
		return false
	}
}

func splitRepoPath(packagePath string) []string {
	packagePath = strings.Trim(packagePath, "/")
	if packagePath == "" || packagePath == "." {
		return nil
	}
	return strings.Split(path.Clean(packagePath), "/")
}

func readModulePath(root string) (string, error) {
	file, err := os.Open(filepath.Join(root, "go.mod"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", nil
}
