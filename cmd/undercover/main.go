package main

import (
	"bufio"
	"fmt"
	"html/template"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type Profile struct {
	FileName string
	Mode     string
	Blocks   []ProfileBlock
}

type ProfileBlock struct {
	StartLine int
	StartCol  int
	EndLine   int
	EndCol    int
	NumStmt   int
	Count     int
}

type InputCoverage struct {
	Name       string
	Profiles   map[string]*Profile
	TotalLines int
	CoveredLines int
}

type FileCoverage struct {
	FileName     string
	Source       []string
	LineInfo     map[int]*LineCoverage
	Inputs       []string
}

type LineCoverage struct {
	Covered      bool
	CoveredBy    map[string]bool
}

func main() {
	log.SetFlags(0)

	if len(os.Args) < 3 {
		log.Fatalf("usage: undercover -o output.html input1.cov input2.cov ...")
	}

	var outputFile string
	var inputFiles []string

	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "-o" {
			if i+1 >= len(os.Args) {
				log.Fatalf("-o requires an argument")
			}
			outputFile = os.Args[i+1]
			i++
		} else {
			inputFiles = append(inputFiles, os.Args[i])
		}
	}

	if outputFile == "" {
		log.Fatalf("output file required (-o)")
	}
	if len(inputFiles) == 0 {
		log.Fatalf("at least one input file required")
	}

	inputs, err := parseInputs(inputFiles)
	if err != nil {
		log.Fatalf("parse inputs: %v", err)
	}

	sortInputs(inputs)

	files, err := aggregateFileCoverage(inputs)
	if err != nil {
		log.Fatalf("aggregate coverage: %v", err)
	}

	if err := generateHTML(outputFile, inputs, files); err != nil {
		log.Fatalf("generate HTML: %v", err)
	}
}

func parseInputs(inputFiles []string) ([]*InputCoverage, error) {
	var inputs []*InputCoverage

	for _, path := range inputFiles {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", path, err)
		}

		input := &InputCoverage{
			Name:     path,
			Profiles: make(map[string]*Profile),
		}

		scanner := bufio.NewScanner(f)
		var mode string

		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "mode: ") {
				mode = strings.TrimPrefix(line, "mode: ")
				continue
			}

			parts := strings.Fields(line)
			if len(parts) < 3 {
				continue
			}

			fileAndRange := parts[0]
			numStmt, _ := strconv.Atoi(parts[1])
			count, _ := strconv.Atoi(parts[2])

			colonIdx := strings.Index(fileAndRange, ":")
			if colonIdx == -1 {
				continue
			}

			fileName := fileAndRange[:colonIdx]
			rangeStr := fileAndRange[colonIdx+1:]

			rangeParts := strings.Split(rangeStr, ",")
			if len(rangeParts) != 2 {
				continue
			}

			startParts := strings.Split(rangeParts[0], ".")
			endParts := strings.Split(rangeParts[1], ".")
			if len(startParts) != 2 || len(endParts) != 2 {
				continue
			}

			startLine, _ := strconv.Atoi(startParts[0])
			startCol, _ := strconv.Atoi(startParts[1])
			endLine, _ := strconv.Atoi(endParts[0])
			endCol, _ := strconv.Atoi(endParts[1])

			profile, ok := input.Profiles[fileName]
			if !ok {
				profile = &Profile{
					FileName: fileName,
					Mode:     mode,
				}
				input.Profiles[fileName] = profile
			}

			profile.Blocks = append(profile.Blocks, ProfileBlock{
				StartLine: startLine,
				StartCol:  startCol,
				EndLine:   endLine,
				EndCol:    endCol,
				NumStmt:   numStmt,
				Count:     count,
			})

			if count > 0 {
				for l := startLine; l <= endLine; l++ {
					input.CoveredLines++
				}
			}
			for l := startLine; l <= endLine; l++ {
				input.TotalLines++
			}
		}

		f.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("scan %s: %w", path, err)
		}

		inputs = append(inputs, input)
	}

	return inputs, nil
}

func sortInputs(inputs []*InputCoverage) {
	sort.Slice(inputs, func(i, j int) bool {
		pctI := float64(0)
		if inputs[i].TotalLines > 0 {
			pctI = float64(inputs[i].CoveredLines) / float64(inputs[i].TotalLines)
		}
		pctJ := float64(0)
		if inputs[j].TotalLines > 0 {
			pctJ = float64(inputs[j].CoveredLines) / float64(inputs[j].TotalLines)
		}
		return pctI < pctJ
	})
}

func aggregateFileCoverage(inputs []*InputCoverage) (map[string]*FileCoverage, error) {
	files := make(map[string]*FileCoverage)

	for _, input := range inputs {
		for fileName, profile := range input.Profiles {
			fc, ok := files[fileName]
			if !ok {
				source, err := readSourceFile(fileName)
				if err != nil {
					return nil, fmt.Errorf("read source %s: %w", fileName, err)
				}
				fc = &FileCoverage{
					FileName: fileName,
					Source:   source,
					LineInfo: make(map[int]*LineCoverage),
				}
				files[fileName] = fc
			}

			found := false
			for _, existingInput := range fc.Inputs {
				if existingInput == input.Name {
					found = true
					break
				}
			}
			if !found {
				fc.Inputs = append(fc.Inputs, input.Name)
			}

			for _, block := range profile.Blocks {
				for l := block.StartLine; l <= block.EndLine; l++ {
					if fc.LineInfo[l] == nil {
						fc.LineInfo[l] = &LineCoverage{
							CoveredBy: make(map[string]bool),
						}
					}
					if block.Count > 0 {
						fc.LineInfo[l].Covered = true
						fc.LineInfo[l].CoveredBy[input.Name] = true
					}
				}
			}
		}
	}

	return files, nil
}

func readSourceFile(fileName string) ([]string, error) {
	actualPath, err := resolveFilePath(fileName)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(actualPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	return lines, scanner.Err()
}

func resolveFilePath(fileName string) (string, error) {
	if _, err := os.Stat(fileName); err == nil {
		return fileName, nil
	}

	cmd := exec.Command("go", "list", "-f", "{{.Dir}}", "./...")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fileName, nil
	}

	dirs := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, dir := range dirs {
		parts := strings.Split(fileName, string(filepath.Separator))
		for i := 0; i < len(parts); i++ {
			potentialPath := filepath.Join(dir, filepath.Join(parts[i:]...))
			if _, err := os.Stat(potentialPath); err == nil {
				return potentialPath, nil
			}
		}
	}

	return fileName, nil
}

func generateHTML(outputPath string, inputs []*InputCoverage, files map[string]*FileCoverage) error {
	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()

	inputMap := make(map[string]int)
	for i, input := range inputs {
		inputMap[input.Name] = i
	}

	sortedFiles := make([]*FileCoverage, 0, len(files))
	for _, fc := range files {
		sortedFiles = append(sortedFiles, fc)
	}
	sort.Slice(sortedFiles, func(i, j int) bool {
		return sortedFiles[i].FileName < sortedFiles[j].FileName
	})

	data := struct {
		Inputs    []*InputCoverage
		Files     []*FileCoverage
		InputMap  map[string]int
	}{
		Inputs:    inputs,
		Files:     sortedFiles,
		InputMap:  inputMap,
	}

	return tmpl.Execute(f, data)
}

var tmpl = template.Must(template.New("html").Funcs(template.FuncMap{
	"add": func(a, b int) int { return a + b },
	"coverageClass": func(lineNum int, fc *FileCoverage) string {
		if lc, ok := fc.LineInfo[lineNum]; ok {
			if lc.Covered {
				return "covered"
			}
			return "uncovered"
		}
		return ""
	},
	"lineCoverage": func(lineNum int, fc *FileCoverage) *LineCoverage {
		return fc.LineInfo[lineNum]
	},
	"hasCoverage": func(inputName string, lc *LineCoverage) bool {
		if lc == nil {
			return false
		}
		return lc.CoveredBy[inputName]
	},
	"percentage": func(covered, total int) string {
		if total == 0 {
			return "0.0%"
		}
		return fmt.Sprintf("%.1f%%", 100.0*float64(covered)/float64(total))
	},
	"inputCount": func(lineNum int, fc *FileCoverage) int {
		if lc, ok := fc.LineInfo[lineNum]; ok {
			return len(lc.CoveredBy)
		}
		return 0
	},
}).Parse(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<title>Go Coverage Report</title>
<style>
* { margin: 0; padding: 0; box-sizing: border-box; }
body { font-family: monospace; font-size: 13px; }

#container {
	display: flex;
	height: 100vh;
}

#sidebar {
	width: 300px;
	border-right: 1px solid #ccc;
	overflow-y: auto;
	background: #f8f8f8;
}

#main {
	flex: 1;
	overflow-y: auto;
	display: flex;
	flex-direction: column;
}

#controls {
	padding: 10px;
	border-bottom: 1px solid #ccc;
	background: #f0f0f0;
}

#content {
	flex: 1;
	overflow-y: auto;
}

.file-item {
	padding: 8px 12px;
	cursor: pointer;
	border-bottom: 1px solid #e0e0e0;
}

.file-item:hover {
	background: #e8e8e8;
}

.file-item.active {
	background: #d0d0ff;
	font-weight: bold;
}

.input-info {
	padding: 8px 12px;
	border-bottom: 1px solid #e0e0e0;
	background: #fff;
}

.input-name {
	font-weight: bold;
	margin-bottom: 4px;
}

.input-stats {
	color: #666;
	font-size: 11px;
}

.file-section {
	display: none;
}

.file-section.active {
	display: block;
}

.source-line {
	display: flex;
	white-space: pre;
	position: relative;
}

.source-line:hover .tooltip {
	display: block;
}

.tooltip {
	display: none;
	position: absolute;
	left: 70px;
	top: 0;
	background: #333;
	color: #fff;
	padding: 4px 8px;
	border-radius: 3px;
	font-size: 11px;
	white-space: nowrap;
	z-index: 1000;
	pointer-events: none;
}

.line-num {
	display: inline-block;
	width: 60px;
	text-align: right;
	padding-right: 10px;
	color: #666;
	background: #f0f0f0;
	border-right: 1px solid #ccc;
	user-select: none;
	position: relative;
}

.input-count {
	position: absolute;
	left: 2px;
	top: 50%;
	transform: translateY(-50%);
	background: #4a90e2;
	color: white;
	font-size: 9px;
	font-weight: bold;
	padding: 1px 4px;
	border-radius: 3px;
	min-width: 14px;
	text-align: center;
}

.line-code {
	flex: 1;
	padding-left: 10px;
	background: white;
}

.source-line.covered .line-num {
	background: #c0ffc0;
}

.source-line.covered .line-code {
	background: #e0ffe0;
}

.source-line.uncovered .line-num {
	background: #ffc0c0;
}

.source-line.uncovered .line-code {
	background: #ffe0e0;
}

#input-filter {
	padding: 4px 8px;
	font-family: monospace;
	font-size: 13px;
}

label {
	margin-right: 8px;
}

h2 {
	padding: 12px;
	background: #f0f0f0;
	border-bottom: 1px solid #ccc;
	position: sticky;
	top: 0;
	z-index: 10;
}

#inputs-header {
	padding: 12px;
	background: #e0e0e0;
	font-weight: bold;
	border-bottom: 1px solid #ccc;
}
</style>
</head>
<body>

<div id="container">
	<div id="sidebar">
		<div id="inputs-header">Files</div>
		{{range $idx, $file := .Files}}
		<div class="file-item" data-file-idx="{{$idx}}">{{$file.FileName}}</div>
		{{end}}
	</div>

	<div id="main">
		<div id="controls">
			<label for="input-filter">Filter by input:</label>
			<select id="input-filter">
				<option value="">All inputs</option>
				{{range .Inputs}}
				<option value="{{.Name}}">{{.Name}}</option>
				{{end}}
			</select>
		</div>

		<div id="content">
			{{range $fileIdx, $file := .Files}}
			<div class="file-section" data-file-idx="{{$fileIdx}}">
				<h2>{{$file.FileName}}</h2>
				{{range $lineIdx, $lineContent := $file.Source}}
				{{$lineNum := add $lineIdx 1}}
				{{$lc := lineCoverage $lineNum $file}}
				<div class="source-line {{coverageClass $lineNum $file}}" data-inputs="{{range $inputName := $file.Inputs}}{{if hasCoverage $inputName $lc}}{{$inputName}}|||{{end}}{{end}}">
					{{if $lc}}{{if $lc.Covered}}<span class="tooltip">Covered by: {{range $inputName := $file.Inputs}}{{if hasCoverage $inputName $lc}}{{$inputName}} {{end}}{{end}}</span>{{end}}{{end}}
					<span class="line-num">{{$count := inputCount $lineNum $file}}{{if gt $count 0}}<span class="input-count">{{$count}}</span>{{end}}{{$lineNum}}</span>
					<span class="line-code">{{$lineContent}}</span>
				</div>
				{{end}}
			</div>
			{{end}}
		</div>
	</div>
</div>

<script>
(function() {
	const fileItems = document.querySelectorAll('.file-item');
	const fileSections = document.querySelectorAll('.file-section');
	const inputFilter = document.getElementById('input-filter');

	function showFile(idx) {
		fileItems.forEach((item, i) => {
			item.classList.toggle('active', i === idx);
		});
		fileSections.forEach((section, i) => {
			section.classList.toggle('active', i === idx);
		});
	}

	fileItems.forEach((item, idx) => {
		item.addEventListener('click', () => showFile(idx));
	});

	if (fileItems.length > 0) {
		showFile(0);
	}

	inputFilter.addEventListener('change', function() {
		const selectedInput = this.value;
		const lines = document.querySelectorAll('.source-line');

		lines.forEach(line => {
			const inputs = line.getAttribute('data-inputs') || '';
			const hasCoverage = inputs.length > 0;
			const isCoverable = line.classList.contains('covered') || line.classList.contains('uncovered');

			if (!selectedInput) {
				if (hasCoverage) {
					line.classList.add('covered');
					line.classList.remove('uncovered');
				} else if (isCoverable) {
					line.classList.add('uncovered');
					line.classList.remove('covered');
				}
			} else {
				const coveredByInput = inputs.split('|||').includes(selectedInput);

				if (coveredByInput) {
					line.classList.add('covered');
					line.classList.remove('uncovered');
				} else if (isCoverable) {
					line.classList.add('uncovered');
					line.classList.remove('covered');
				}
			}
		});
	});
})();
</script>

</body>
</html>
`))
