package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

var issueExternalCmd = &cobra.Command{
	Use:   "external",
	Short: "Create or read issues by immutable external reference",
}

var issueExternalCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Atomically create or return an externally referenced issue",
	RunE:  runIssueExternalCreate,
}

var issueExternalGetCmd = &cobra.Command{
	Use:   "get <provider> <instance-id> <external-id>",
	Short: "Get an issue by its immutable external reference",
	Args:  exactArgs(3),
	RunE:  runIssueExternalGet,
}

func init() {
	issueCmd.AddCommand(issueExternalCmd)
	issueExternalCmd.AddCommand(issueExternalCreateCmd, issueExternalGetCmd)

	issueExternalCreateCmd.Flags().String("provider", "", "External provider key (required)")
	issueExternalCreateCmd.Flags().String("instance-id", "", "Stable external system instance ID (required)")
	issueExternalCreateCmd.Flags().String("external-id", "", "Stable external task ID (required)")
	issueExternalCreateCmd.Flags().String("external-url", "", "Backlink without credentials or query parameters")
	issueExternalCreateCmd.Flags().String("title", "", "Issue title (required)")
	issueExternalCreateCmd.Flags().String("description", "", "Issue description")
	issueExternalCreateCmd.Flags().Bool("description-stdin", false, "Read issue description from stdin")
	issueExternalCreateCmd.Flags().String("description-file", "", "Read issue description from a UTF-8 file inside the working directory")
	issueExternalCreateCmd.Flags().Bool("allow-external-file", false, "Allow --description-file outside the working directory")
	issueExternalCreateCmd.Flags().String("status", "", "Issue status (default: todo)")
	issueExternalCreateCmd.Flags().String("priority", "", "Issue priority (default: none)")
	issueExternalCreateCmd.Flags().String("assignee-type", "", "Assignee type: member, agent, or squad")
	issueExternalCreateCmd.Flags().String("assignee-id", "", "Assignee UUID (requires --assignee-type)")
	issueExternalCreateCmd.Flags().String("project-id", "", "Project UUID")
	issueExternalCreateCmd.Flags().String("output", "json", "Output format: table or json")
	issueExternalGetCmd.Flags().String("output", "json", "Output format: table or json")
}

func runIssueExternalCreate(cmd *cobra.Command, _ []string) error {
	provider, _ := cmd.Flags().GetString("provider")
	instanceID, _ := cmd.Flags().GetString("instance-id")
	externalID, _ := cmd.Flags().GetString("external-id")
	title, _ := cmd.Flags().GetString("title")
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(instanceID) == "" || strings.TrimSpace(externalID) == "" || strings.TrimSpace(title) == "" {
		return fmt.Errorf("--provider, --instance-id, --external-id, and --title are required")
	}
	status, _ := cmd.Flags().GetString("status")
	if status != "" {
		if err := validateIssueStatus(status); err != nil {
			return err
		}
	}
	priority, _ := cmd.Flags().GetString("priority")
	if priority != "" {
		if err := validateIssuePriority(priority); err != nil {
			return err
		}
	}
	assigneeType, _ := cmd.Flags().GetString("assignee-type")
	assigneeID, _ := cmd.Flags().GetString("assignee-id")
	if (assigneeType == "") != (assigneeID == "") {
		return fmt.Errorf("--assignee-type and --assignee-id must be provided together")
	}

	issue := map[string]any{"title": title}
	description, hasDescription, err := resolveTextFlag(cmd, "description")
	if err != nil {
		return err
	}
	if hasDescription {
		issue["description"] = description
	}
	if status != "" {
		issue["status"] = status
	}
	if priority != "" {
		issue["priority"] = priority
	}
	if assigneeType != "" {
		issue["assignee_type"] = assigneeType
		issue["assignee_id"] = assigneeID
	}
	if projectID, _ := cmd.Flags().GetString("project-id"); projectID != "" {
		issue["project_id"] = projectID
	}
	ref := map[string]any{"provider": provider, "instance_id": instanceID, "external_id": externalID}
	if externalURL, _ := cmd.Flags().GetString("external-url"); externalURL != "" {
		ref["external_url"] = externalURL
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.PostJSON(ctx, "/api/integration/issues", map[string]any{
		"external_issue_ref": ref,
		"issue":              issue,
	}, &result); err != nil {
		return fmt.Errorf("create external issue: %w", err)
	}
	return printExternalIssueResult(cmd, result)
}

func runIssueExternalGet(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	query := url.Values{"provider": {args[0]}, "instance_id": {args[1]}, "external_id": {args[2]}}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.GetJSON(ctx, "/api/integration/issues?"+query.Encode(), &result); err != nil {
		return fmt.Errorf("get external issue: %w", err)
	}
	return printExternalIssueResult(cmd, result)
}

func printExternalIssueResult(cmd *cobra.Command, result map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		issue, _ := result["issue"].(map[string]any)
		ref, _ := result["external_issue_ref"].(map[string]any)
		cli.PrintTable(os.Stdout, []string{"EXTERNAL REF", "ISSUE", "STATUS", "LINK"}, [][]string{{
			fmt.Sprintf("%s/%s/%s", strVal(ref, "provider"), strVal(ref, "instance_id"), strVal(ref, "external_id")),
			issueDisplayKey(issue), strVal(issue, "status"), firstNonEmpty(strVal(result, "issue_url"), strVal(result, "issue_path")),
		}})
		return nil
	}
	if output != "json" {
		return fmt.Errorf("invalid output %q; valid values: table, json", output)
	}
	return cli.PrintJSON(os.Stdout, result)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
