package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const overallThreshold = 82.0

var packageThresholds = map[string]float64{
	"cmd/bot":            75.0,
	"internal/store":     78.0,
	"internal/bot":       85.0,
	"internal/config":    85.0,
	"internal/captcha":   93.0,
	"internal/blacklist": 97.0,
}

type coverageCounter struct {
	total   int
	covered int
}

func main() {
	coverPaths := []string{"coverage.out"}
	if len(os.Args) > 1 {
		coverPaths = os.Args[1:]
	}

	counters, overall, err := parseCoverageProfiles(coverPaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coveragegate: %v\n", err)
		os.Exit(1)
	}

	keys := make([]string, 0, len(packageThresholds))
	for key := range packageThresholds {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	failed := false
	for _, pkg := range keys {
		counter := counters[pkg]
		coverage := percent(counter)
		fmt.Printf("%-20s %.1f%% (threshold %.1f%%)\n", pkg, coverage, packageThresholds[pkg])
		if coverage < packageThresholds[pkg] {
			failed = true
		}
	}

	overallCoverage := percent(overall)
	fmt.Printf("%-20s %.1f%% (threshold %.1f%%)\n", "overall", overallCoverage, overallThreshold)
	if overallCoverage < overallThreshold {
		failed = true
	}

	if failed {
		os.Exit(1)
	}
}

func parseCoverageProfiles(paths []string) (map[string]coverageCounter, coverageCounter, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, coverageCounter{}, fmt.Errorf("get working directory: %w", err)
	}

	modulePath, err := readModulePath(filepath.Join(cwd, "go.mod"))
	if err != nil {
		return nil, coverageCounter{}, err
	}

	counters := make(map[string]coverageCounter)
	var overall coverageCounter

	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return nil, coverageCounter{}, fmt.Errorf("open coverage profile %s: %w", path, err)
		}

		scanner := bufio.NewScanner(file)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "mode:") {
				continue
			}

			counterPath, numStmts, count, err := parseCoverageLine(line)
			if err != nil {
				_ = file.Close()
				return nil, coverageCounter{}, fmt.Errorf("parse %s line %d: %w", path, lineNumber, err)
			}

			relPath := normalizeCoveragePath(counterPath, cwd, modulePath)
			pkgDir := filepath.ToSlash(filepath.Dir(relPath))
			counter := counters[pkgDir]
			counter.total += numStmts
			if count > 0 {
				counter.covered += numStmts
			}
			counters[pkgDir] = counter

			overall.total += numStmts
			if count > 0 {
				overall.covered += numStmts
			}
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			return nil, coverageCounter{}, fmt.Errorf("scan coverage profile %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return nil, coverageCounter{}, fmt.Errorf("close coverage profile %s: %w", path, err)
		}
	}

	return counters, overall, nil
}

func parseCoverageLine(line string) (string, int, int, error) {
	separator := strings.LastIndex(line, ":")
	if separator == -1 {
		return "", 0, 0, fmt.Errorf("missing separator")
	}

	path := line[:separator]
	fields := strings.Fields(line[separator+1:])
	if len(fields) != 3 {
		return "", 0, 0, fmt.Errorf("unexpected fields %q", line)
	}

	numStmts, err := strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, 0, fmt.Errorf("parse statements: %w", err)
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		return "", 0, 0, fmt.Errorf("parse count: %w", err)
	}

	return path, numStmts, count, nil
}

func normalizeCoveragePath(path, cwd, modulePath string) string {
	if strings.HasPrefix(filepath.ToSlash(path), modulePath+"/") {
		return strings.TrimPrefix(filepath.ToSlash(path), modulePath+"/")
	}

	if filepath.IsAbs(path) {
		if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}

	return filepath.ToSlash(path)
}

func readModulePath(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read go.mod: %w", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}

	return "", fmt.Errorf("module path not found in go.mod")
}

func percent(counter coverageCounter) float64 {
	if counter.total == 0 {
		return 0
	}
	return float64(counter.covered) * 100 / float64(counter.total)
}
