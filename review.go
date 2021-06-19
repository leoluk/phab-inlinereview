// inlinereview automatically posts comments added to the current Git working copy
// to Phabricator as inline comments. The following formats are recognized,
// including multiline comments:
//
//   //% body
//   #% body
//
// With this workflow, you do the initial code review in IntelliJ, upload
// the comments and then address follow-up comments and updates using the native
// Phabricator UI. Existing comments are not replaced, if you re-run this
// script, you will end up with duplicate comments.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"

	godiff "github.com/sourcegraph/go-diff/diff"
)

// Input for differential.createinline conduit method
type conduitCreateInlineComment struct {
	RevisionID string `json:"revisionID"`
	FilePath   string `json:"filePath"`
	IsNewFile  int    `json:"isNewFile"`
	LineNumber int32  `json:"lineNumber"`
	Content    string `json:"content"`
}

// Generic conduit response
type conduitResponse struct {
	Error        string `json:"error"`
	ErrorMessage string `json:"errorMessage"`
}

// Details about the current revision as returned by "arc which"
type revisionDetails struct {
	// Differential revision (not diff!) ID, without the leading "D".
	revisionID string
	// Author username
	author string
	// Revision title (i.e. commit subject)
	title string
	// Human-readable description of the revision
	humanReadable string
}

var (
	// reMatchComment matches the text inside a comment block:
	//	+//% Comment on line 91
	//  +//% Multiline comment!
	reMatchComment, _ = regexp.Compile(`(?m)^\+\s*(?://|#)%\s+(.+)$`)
	// reWhichRevision matches "arc which" output the extract
	// revision ID, author, title and reason from the "MATCHING REVISIONS"
	// section of the output.
	reWhichRevision, _ = regexp.Compile(
		`(?ms)MATCHING REVISIONS.+D(\d+) \((\w+)\) (.+?)$.+Reason: (.*?)$`)
)

const (
	whichRevisionExisting = "A git commit or tree hash in the commit range is already attached to the Differential revision."
)

func getDiff(base string) ([]byte, error) {
	cmd := exec.Command("git", "diff", "-U0", "--no-prefix", base)

	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	return out, nil
}

// parseHunk returns the stripped comment text for a given hunk in a //% comment.
func parseHunkToText(body []byte) string {
	lines := make([]string, 0, 0)
	m := reMatchComment.FindAllSubmatch(body, -1)
	for _, l := range m {
		lines = append(lines, string(l[1]))
	}

	// The trim is not necessary with properly formatted comments.
	return strings.Trim(strings.Join(lines, "\n"), " \t")
}

// createInlineDiff creates a Differential inline comment for a given
// revision, using the current working directory's arcanist context.
func createInlineDiff(revision string, filePath string, lineNumber int32, content string) error {
	args := conduitCreateInlineComment{
		RevisionID: revision,
		FilePath:   filePath,
		IsNewFile:  1,
		LineNumber: lineNumber,
		Content:    content,
	}

	b, err := json.Marshal(args)
	if err != nil {
		return err
	}

	cmd := exec.Command("arc", "call-conduit", "differential.createinline")
	cmd.Stdin = bytes.NewReader(b)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to call conduit: %w", err)
	}

	// Check if the conduit call succeeded (arc call-conduit does not
	// return an error exit code if it failed)
	var response conduitResponse
	err = json.Unmarshal(out, &response)
	if err != nil {
		return err
	}

	if response.Error != "" {
		log.Println("payload: ", string(b))
		log.Println("conduit: ", string(out))
		return fmt.Errorf("conduit returned %s: %s",
			response.Error, response.ErrorMessage)
	}

	return nil
}

// getRepoRevision calls "arc which" in the current working directory
// and parses its "MATCHING REVISIONS" section into a struct.
//
// A better way to do this would be to either implement the whole thing in
// arcanist (where this information is a simple API call away),
// or to implement a custom arcanist action that returns machine-readable,
// but that would be a lot more effort, so 🤷
func getRepoRevisionDetails() (revisionDetails, error) {
	cmd := exec.Command("arc", "which")
	out, err := cmd.Output()
	if err != nil {
		return revisionDetails{}, err
	}

	m := reWhichRevision.FindAllSubmatch(out, -1)
	if m == nil {
		panic("reWhichRevision failed to match arc which output")
	}

	return revisionDetails{
		humanReadable: string(m[0][0]),
		revisionID:    string(m[0][1]),
		author:        string(m[0][2]),
		title:         string(m[0][3]),
	}, nil
}

// arcBrowse calls "arc browse" to open a specific object in the browser.
// "HEAD" will bring up the revision at the top of the stack.
func arcBrowse(object string) error {
	cmd := exec.Command("arc", "browse", object)
	return cmd.Run()
}

func main() {
	revision, err := getRepoRevisionDetails()
	if err != nil {
		log.Fatal("Failed to get revision details: ", err)
	}

	log.Printf("Selected revision D%s (%s) - %s",
		revision.revisionID, revision.author, revision.title)

	diff, err := getDiff("HEAD")
	if err != nil {
		log.Fatal("Failed to get diff: ", diff)
	}

	p, err := godiff.ParseMultiFileDiff(diff)
	if err != nil {
		log.Fatal("Failed to parse diff: ", err)
	}

	for _, d := range p {
		for _, h := range d.Hunks {
			line := h.OrigStartLine + 1
			body := parseHunkToText(h.Body)

			if strings.TrimSpace(body) == "" {
				continue
			}

			log.Printf("%s:%d\t%s\n", d.OrigName, line, body)

			err = createInlineDiff(revision.revisionID, d.OrigName, line, body)
			if err != nil {
				log.Printf("error creating inline comment: %v", err)
			}
		}
	}

	// Open revision in browser
	if err := arcBrowse("HEAD"); err != nil {
		log.Fatal("Failed to open revision in browser: ", err)
	}
}
