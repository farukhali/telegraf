package main

import (
	"bufio"
	"bytes"
	"fmt" //nolint:revive
	"html/template"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var (
	separator = "@@__CHGLOG__@@"
	delimiter = "@@__CHGLOG_DELIMITER__@@"

	hashField    = "HASH"
	authorField  = "AUTHOR"
	subjectField = "SUBJECT"
	bodyField    = "BODY"

	ignoreList = []string{
		`update etc/telegraf.conf and etc/telegraf_windows.conf`,
		`update configs`,
	}
)

type Commit struct {
	Hash       string
	AuthorName string
	Type       string // (e.g. `feat`)
	Scope      string // (e.g. `core`)
	Subject    string // (e.g. `Add new feature`)
}

func ParseCommits() ([]Commit, error) {
	latestTagHash, err := exec.Command("git", "rev-list", "--tags", "--max-count=1").Output()
	if err != nil {
		return nil, err
	}
	tag, err := exec.Command("git", "describe", "--tags", strings.TrimSuffix(string(latestTagHash), "\n")).Output()
	if err != nil {
		return nil, err
	}
	latestTag := strings.TrimSuffix(string(tag), "\n")

	hashFormat := hashField + ":%h"
	authorFormat := authorField + ":%an"
	subjectFormat := subjectField + ":%s"
	bodyFormat := bodyField + ":%b"

	logFormat := separator + strings.Join([]string{
		hashFormat,
		authorFormat,
		subjectFormat,
		bodyFormat,
	}, delimiter)

	logs, err := exec.Command("git", "log", fmt.Sprintf("--pretty=%s", logFormat), fmt.Sprintf("%s...", latestTag)).Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(logs), separator)
	lines = lines[1:]

	commits := make([]Commit, len(lines))

	for i, line := range lines {
		commit, err := parseCommit(line, delimiter)
		if err != nil {
			return nil, err
		}

		var ignore bool
		for _, substring := range ignoreList {
			if strings.Contains(commit.Subject, substring) {
				ignore = true
				break
			}
		}

		if ignore {
			continue
		}

		commits[i] = commit
	}

	return commits, nil
}

func parseCommit(input string, delimiter string) (Commit, error) {
	commit := Commit{}
	tokens := strings.Split(input, delimiter)

	for _, token := range tokens {
		firstSep := strings.Index(token, ":")
		field := token[0:firstSep]
		value := strings.TrimSpace(token[firstSep+1:])

		switch field {
		case hashField:
			commit.Hash = value
		case authorField:
			commit.AuthorName = value
		case subjectField:
			reHeader := regexp.MustCompile(`^(\w*)(?:\(([\w\$\.\-\*\s]*)\))?\:\s(.*)$`)
			res := reHeader.FindAllStringSubmatch(value, -1)
			if len(res) > 0 && len(res[0]) == 4 {
				commit.Type = res[0][1]
				commit.Scope = res[0][2]
				commit.Subject = res[0][3]
			}
		}
	}

	if commit.Scope == "" {
		commit.Scope = detectScope(commit.Hash)
	}

	return commit, nil
}

func detectScope(hash string) string {
	var scope string

	changedFiles, err := exec.Command("git", "diff-tree", "--no-commit-id", "--name-only", "-r", hash).Output()
	if err != nil {
		return ""
	}

	if len(changedFiles) == 0 {
		return ""
	}

	r, _ := regexp.Compile(`plugins\/(.*){2}\/`)
	changedFilesSlice := strings.Split(string(changedFiles), "\n")
	for _, c := range changedFilesSlice {
		if changedFilePath := r.FindString(c); changedFilePath != "" {
			pluginPath := strings.Split(changedFilePath, "/")
			if len(pluginPath) < 3 && (pluginPath[2] == "" || pluginPath[2] == "all") {
				continue
			}
			scope = fmt.Sprintf("%s.%s", pluginPath[1], pluginPath[2])
			break
		}
	}

	return scope
}

type NewChanges struct {
	Version      string
	Date         string
	CommitGroups []CommitGroup
}

type CommitGroup struct {
	Title   string
	Commits []Commit
}

func CreateCommitGroups(commits []Commit) []CommitGroup {
	var commitGroups []CommitGroup

	featGroup := CommitGroup{
		Title: "Features",
	}

	bugGroup := CommitGroup{
		Title: "Bugfixes",
	}

	for _, c := range commits {
		switch c.Type {
		case "fix":
			bugGroup.Commits = append(bugGroup.Commits, c)
		case "feat":
			featGroup.Commits = append(featGroup.Commits, c)
		}
	}

	commitGroups = append(commitGroups, bugGroup, featGroup)

	return commitGroups
}

func AppendToChangelog(change []byte) error {
	changelogFile, err := os.Open("CHANGELOG.md")
	if err != nil {
		return err
	}
	defer changelogFile.Close()

	var c []byte
	buf := bytes.NewBuffer(c)
	scanner := bufio.NewScanner(changelogFile)
	var read bool
	for scanner.Scan() {
		if !read && scanner.Text() == "# Changelog" {
			read = true
			continue
		}

		if read {
			_, err := buf.Write(scanner.Bytes())
			if err != nil {
				return err
			}
			_, err = buf.WriteString("\n")
			if err != nil {
				return err
			}
		}
	}

	header := `<!-- markdownlint-disable MD024 -->

# Changelog
`

	out := fmt.Sprintf("%s\n%s", header, string(change))
	final := append([]byte(out), buf.Bytes()[:]...)

	err = os.WriteFile("CHANGELOG.md", final, 0664)
	if err != nil {
		return err
	}

	return nil
}

func main() {
	ver, err := os.ReadFile("build_version.txt")
	if err != nil {
		log.Fatal(err)
	}

	version := fmt.Sprintf("v%s", strings.TrimSuffix(string(ver), "\n"))

	commits, err := ParseCommits()
	if err != nil {
		log.Fatal(err)
	}

	commitGroups := CreateCommitGroups(commits)

	newChanges := NewChanges{
		Version:      version,
		Date:         time.Now().Format("2006-01-02"),
		CommitGroups: commitGroups,
	}

	temp := template.Must(template.ParseFiles("scripts/generate_changelog/CHANGELOG.go.tmpl"))
	var out bytes.Buffer
	err = temp.Execute(&out, newChanges)
	if err != nil {
		log.Fatal(err)
	}

	err = AppendToChangelog(out.Bytes())
	if err != nil {
		log.Fatal(err)
	}
}
