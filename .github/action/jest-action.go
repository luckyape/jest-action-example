package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/go-github/v28/github"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v2"
)

type Report struct {
	NumFailedTests      int
	NumPassedTests      int
	NumTotalTests       int
	NumFailedTestSuites int
	NumPassedTestSuites int
	NumTotalTestSuites  int
	Success             bool
	TestResults         []*TestResult
}

type TestResult struct {
	AssertionResults []*AssertionResult
	Message          string
	FilePath         string `json:"name"`
	Status           string
	Summary          string
}

type AssertionResult struct {
	AncestorTitles  []string
	FailureMessages []string
	FullName        string
	Location        Location
	Status          string
	Title           string
}

type Location struct {
	Column int
	Line   int
}

func main() {
	// read and decode the Jest result from stdin
	var report Report
	err := json.NewDecoder(os.Stdin).Decode(&report)
	if err != nil {
		log.Fatal(err)
	}

	checkName, err := extractCheckName()
	if err != nil {
		log.Fatal(err)
	}

	log.Println("CHECK NNAAAAAMMMEE IS:", checkName)

	// nothing to do, if the tests succeeded
	if report.Success {
		return
	}

	ctx := context.Background()
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: os.Getenv("GITHUB_TOKEN")},
	)
	client := github.NewClient(oauth2.NewClient(ctx, ts))

	head := os.Getenv("GITHUB_SHA")
	repoParts := strings.SplitN(os.Getenv("GITHUB_REPOSITORY"), "/", 2)
	owner := repoParts[0]
	repoName := repoParts[1]

	// find the action's checkrun
	result, _, err := client.Checks.ListCheckRunsForRef(ctx, owner, repoName, head, &github.ListCheckRunsOptions{
		// CheckName: github.String(checkName),
		// HeadSHA:   github.String(head),
		// Status:    github.String("in_progress"),
	})
	if err != nil {
		log.Fatal(err)
	}

	for _, run := range result.CheckRuns {
		fmt.Println(run)
	}

	result2, _, err := client.Checks.ListCheckSuitesForRef(ctx, owner, repoName, head, nil)
	if err != nil {
		log.Fatal(err)
	}

	for _, suite := range result2.CheckSuites {
		fmt.Println(suite)
	}

	if len(result.CheckRuns) == 0 {
		log.Fatalf("Unable to find check run for action: %s", checkName)
	}
	checkRun := result.CheckRuns[0]

	// add annotations for test failures
	workspacePath := os.Getenv("GITHUB_WORKSPACE") + "/"
	var annotations []*github.CheckRunAnnotation
	for _, t := range report.TestResults {
		if t.Status == "passed" {
			continue
		}

		path := strings.TrimPrefix(t.FilePath, workspacePath)

		if len(t.AssertionResults) > 0 {
			for _, a := range t.AssertionResults {
				if a.Status == "passed" {
					continue
				}

				if len(a.FailureMessages) == 0 {
					a.FailureMessages = append(a.FailureMessages, a.FullName)
				}

				annotations = append(annotations, &github.CheckRunAnnotation{
					Path:            github.String(path),
					StartLine:       github.Int(a.Location.Line),
					EndLine:         github.Int(a.Location.Line),
					AnnotationLevel: github.String("failure"),
					Title:           github.String(a.FullName),
					Message:         github.String(strings.Join(a.FailureMessages, "\n\n")),
				})
			}
		} else {
			// usually the case for failed test suites
			annotations = append(annotations, &github.CheckRunAnnotation{
				Path:            github.String(path),
				StartLine:       github.Int(1),
				EndLine:         github.Int(1),
				AnnotationLevel: github.String("failure"),
				Title:           github.String("Test Suite Error"),
				Message:         github.String(t.Message),
			})
		}
	}

	summary := fmt.Sprintf(
		"Test Suites: %d failed, %d passed, %d total\n",
		report.NumFailedTests,
		report.NumPassedTests,
		report.NumTotalTests,
	)
	summary += fmt.Sprintf(
		"Tests: %d failed, %d passed, %d total",
		report.NumFailedTestSuites,
		report.NumPassedTestSuites,
		report.NumTotalTestSuites,
	)

	// add annotations in #50 chunks
	for i := 0; i < len(annotations); i += 50 {
		end := i + 50

		if end > len(annotations) {
			end = len(annotations)
		}

		output := &github.CheckRunOutput{
			Title:       github.String("Result"),
			Summary:     github.String(summary),
			Annotations: annotations[i:end],
		}

		_, _, err = client.Checks.UpdateCheckRun(ctx, owner, repoName, checkRun.GetID(), github.UpdateCheckRunOptions{
			Name:    checkName,
			HeadSHA: github.String(head),
			Output:  output,
		})
		if err != nil {
			log.Fatal(err)
		}
	}

	log.Fatal(summary)
}

func extractCheckName() (string, error) {
	workflowName := os.Getenv("GITHUB_WORKFLOW")
	// run1 -> 1
	stepIndex, err := strconv.Atoi(strings.TrimPrefix(os.Getenv("GITHUB_ACTION"), "run"))
	if err != nil {
		return "", err
	}

	stepIndex--

	// go through all workflow files
	files, err := ioutil.ReadDir("./.github/workflows")
	if err != nil {
		return "", err
	}

	type Workflow struct {
		Name string
		Jobs map[string]struct {
			Name  string
			Steps []struct {
				Uses *string
			}
		}
	}

	for _, f := range files {
		if filepath.Ext(f.Name()) != "yml" {
			continue
		}

		data, err := ioutil.ReadFile("./.github/workflows/" + f.Name())
		if err != nil {
			return "", err
		}

		var workflow Workflow
		if err := yaml.Unmarshal(data, &workflow); err != nil {
			return "", err
		}

		if workflow.Name != workflowName {
			continue
		}

		for _, job := range workflow.Jobs {
			if len(job.Steps) <= stepIndex {
				continue
			}

			step := job.Steps[stepIndex]
			if step.Uses == nil {
				continue
			}

			if strings.HasPrefix(*step.Uses, "./.github/action") {
				return job.Name, nil
			}
		}
	}

	return "", fmt.Errorf("Could not find check name")
}
