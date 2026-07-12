package helper

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/fprl/ship/internal/store"
	"github.com/fprl/ship/internal/utils"
)

type approvalCmd struct {
	MemberFingerprint string             `name:"member-fingerprint" hidden:"" help:"Caller SSH public key fingerprint."`
	List              approvalListCmd    `cmd:"list" help:"List pending approval requests."`
	Approve           approvalApproveCmd `cmd:"approve" help:"Approve one pending request."`
}

func (c approvalCmd) BeforeApply() error {
	return requireRoot()
}

type approvalListCmd struct {
	JSON bool `name:"json" help:"Emit structured JSON instead of plain text."`
}

type approvalApproveCmd struct {
	ID string `arg:"" help:"Approval request id."`
}

type approvalListPayload struct {
	Approvals []approvalListRow `json:"approvals"`
}

type approvalListRow struct {
	ID      string `json:"id"`
	Member  string `json:"member"`
	Role    string `json:"role"`
	Request string `json:"request"`
	Expires string `json:"expires"`
}

func (c approvalCmd) AfterApply() error {
	setServerMemberFingerprint(c.MemberFingerprint)
	return nil
}

func (c approvalListCmd) Run() error {
	if _, err := authorizeApprovalList(); err != nil {
		utils.DieError(err, 1)
	}
	requests, err := pendingApprovals()
	if err != nil {
		utils.DieError(err, 1)
	}
	rows := approvalRows(requests)
	if c.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		return enc.Encode(approvalListPayload{Approvals: rows})
	}
	if len(rows) > 0 {
		fmt.Println("ID MEMBER REQUEST EXPIRES")
	}
	for _, row := range rows {
		fmt.Println(formatApprovalRow(row))
	}
	return nil
}

func (c approvalApproveCmd) Run() error {
	approver, err := authorizeApprovalGrant(c.ID)
	if err != nil {
		utils.DieError(err, 1)
	}
	request, err := approveRequest(c.ID, approver)
	if err != nil {
		utils.DieError(err, 1)
	}
	fmt.Printf("approved %s for %s (%s)\n", request.ID, request.Member.Name, request.Target.Summary)
	return nil
}

func approvalRows(requests []store.ApprovalRequest) []approvalListRow {
	rows := make([]approvalListRow, 0, len(requests))
	for _, request := range requests {
		rows = append(rows, approvalListRow{
			ID:      request.ID,
			Member:  request.Member.Name,
			Role:    string(request.Member.Role),
			Request: request.Target.Summary,
			Expires: request.ExpiresAt,
		})
	}
	return rows
}

func formatApprovalRow(row approvalListRow) string {
	return row.ID + " " + row.Member + " " + row.Request + " " + row.Expires
}
