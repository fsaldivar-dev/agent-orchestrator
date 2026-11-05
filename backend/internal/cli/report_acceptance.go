package cli

import (
	"errors"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func addReportReviewCommands(parent *cobra.Command, c *commandContext) {
	var project string
	list := &cobra.Command{Use: "list", Short: "List project reports for director review", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(project) == "" {
			project = os.Getenv("AO_PROJECT_ID")
		}
		if project == "" {
			return usageError{errors.New("--project or AO_PROJECT_ID is required")}
		}
		var result any
		if err := c.getJSON(cmd.Context(), "reports?projectId="+url.QueryEscape(project), &result); err != nil {
			return err
		}
		return writeJSON(cmd.OutOrStdout(), result)
	}}
	list.Flags().StringVar(&project, "project", "", "Project id")
	var note, evidence string
	accept := &cobra.Command{Use: "accept <report-id>", Short: "Accept an artifact delivery after director review", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		reviewer := os.Getenv("AO_SESSION_ID")
		if reviewer == "" || strings.TrimSpace(note) == "" || strings.TrimSpace(evidence) == "" {
			return usageError{errors.New("AO_SESSION_ID, --note and --evidence are required")}
		}
		var result reportAPIResponse
		return c.postJSON(cmd.Context(), "reports/accept", struct {
			ReportID          string `json:"reportId"`
			ReviewerSessionID string `json:"reviewerSessionId"`
			Note              string `json:"note"`
			Evidence          string `json:"evidence"`
		}{args[0], reviewer, note, evidence}, &result)
	}}
	accept.Flags().StringVar(&note, "note", "", "Review findings and acceptance rationale")
	accept.Flags().StringVar(&evidence, "evidence", "", "Validation evidence references")
	parent.AddCommand(list, accept)
}
