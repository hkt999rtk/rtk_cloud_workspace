package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const multicloudLiveConfirmation = "video-cloud-staging-lke"

var (
	multicloudViewerEmailResolver     = multicloudRunScopedViewerEmail
	multicloudInvitationWaiterFactory = multicloudInvitationTokenWaiter
)

type multicloudLiveHTTPClient struct {
	baseURL string
	client  *http.Client
}

type multicloudLiveScenarioInput struct {
	runID       string
	ownerToken  string
	ownerUserID string
	viewerToken string
	viewerID    string
	viewerEmail string
	inviteToken func() (string, error)
}

type multicloudLiveScenarioResult struct {
	CloudID       string            `json:"cloud_id"`
	OperationID   string            `json:"operation_id"`
	Lifecycle     map[string]string `json:"lifecycle"`
	Sharing       map[string]string `json:"sharing"`
	OwnedCount    int               `json:"owned_count_after_create"`
	OwnedLimit    int               `json:"owned_limit"`
	DeletionState string            `json:"deletion_state"`
}

type multicloudLiveEvidence struct {
	Schema         string                       `json:"schema"`
	RunID          string                       `json:"run_id"`
	Environment    string                       `json:"environment"`
	Status         string                       `json:"status"`
	StartedAt      string                       `json:"started_at"`
	CompletedAt    string                       `json:"completed_at"`
	Workflows      map[string]map[string]string `json:"workflows"`
	WorkflowSlices map[string]map[string]string `json:"workflow_slices"`
	Facts          map[string]any               `json:"facts"`
}

func runTestMulticloud(args []string) error {
	fs := flag.NewFlagSet("test-multicloud", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	profile := fs.String("profile", "staging-live", "qualification profile; currently staging-live")
	runID := fs.String("run-id", "", "lowercase run-scoped identifier")
	workspaceFlag := fs.String("workspace", "", "workspace root")
	envRootFlag := fs.String("env-root", "", "staging environment root")
	brandname := fs.String("brandname", "", "existing formal-owner Brand Cloud fixture")
	outputDir := fs.String("output-dir", "", "redacted evidence directory")
	runLive := fs.Bool("run", false, "execute the deployed staging qualification")
	confirm := fs.String("confirm", "", "run mode confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *profile != "staging-live" {
		return fmt.Errorf("unsupported multicloud profile %q; use staging-live", *profile)
	}
	if strings.TrimSpace(*runID) == "" {
		*runID = time.Now().UTC().Format("20060102t150405z") + "-multicloud"
	}
	if !loadRunIDPattern.MatchString(*runID) {
		return errors.New("--run-id must use lowercase letters, digits, and hyphens")
	}
	if !*runLive {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"profile": *profile, "run_id": *runID, "mode": "plan", "status": "READY",
			"workflows":       []string{"WF-MULTICLOUD-LIFECYCLE-001"},
			"workflow_slices": []string{"WF-MULTICLOUD-SHARING-001#cloud-membership"},
		})
	}
	if *confirm != multicloudLiveConfirmation {
		return fmt.Errorf("--confirm must equal %s", multicloudLiveConfirmation)
	}
	if strings.TrimSpace(*brandname) == "" || strings.TrimSpace(*envRootFlag) == "" {
		return errors.New("run mode requires --brandname and --env-root")
	}
	workspace := strings.TrimSpace(*workspaceFlag)
	if workspace == "" {
		var err error
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	ctx, err := accountManagerContextFromFlags(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	defer ctx.Close()
	if !strings.EqualFold(firstNonEmpty(ctx.StackValues["CLOUD_ENV_NAME"], "staging"), "staging") {
		return errors.New("staging-live qualification refuses a non-staging environment")
	}
	store, err := openTestDataStore(ctx.EnvRoot, *brandname)
	if err != nil {
		return err
	}
	defer store.Close()
	owner, err := store.ReadUserByRole(*brandname, "owner")
	if err != nil {
		return err
	}
	platformSession, err := accountLoginSession(ctx, func(string, ...any) {})
	if err != nil {
		return err
	}
	ownerLogin, err := accountLoginUserFull(ctx, "", owner.Email, owner.Password, "")
	if err != nil {
		return fmt.Errorf("login formal owner: %w", err)
	}
	originalCloud, err := accountFindBrandCloud(ctx, platformSession.AccessToken, *brandname)
	if err != nil {
		return err
	}
	viewerEmail, err := multicloudViewerEmailResolver(ctx, *runID)
	if err != nil {
		return err
	}
	viewerPassword, err := randomPassword()
	if err != nil {
		return err
	}
	createdViewer, err := accountCreateUser(ctx, &platformSession, func(string, ...any) {}, stringValue(originalCloud["id"]), viewerEmail, "Multi-cloud qualification viewer", viewerPassword, "member", true)
	if err != nil {
		return fmt.Errorf("create audited staging viewer: %w", err)
	}
	viewerLogin, err := accountLoginUserFull(ctx, "", viewerEmail, viewerPassword, "")
	if err != nil {
		return fmt.Errorf("login verified viewer fixture: %w", err)
	}
	if ownerLogin.User.ID != owner.UserID || viewerLogin.User.ID != createdViewer.UserID {
		return errors.New("global login identity does not match the exact provisioned global user_id")
	}
	waitInvitation, err := multicloudInvitationWaiterFactory(workspace, ctx, viewerEmail)
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	result, err := runMulticloudLiveScenario(context.Background(), multicloudLiveHTTPClient{
		baseURL: strings.TrimRight(ctx.BaseURL, "/"),
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("unexpected redirect")
		}},
	}, multicloudLiveScenarioInput{
		runID: *runID, ownerToken: ownerLogin.Tokens.AccessToken, ownerUserID: ownerLogin.User.ID,
		viewerToken: viewerLogin.Tokens.AccessToken, viewerID: viewerLogin.User.ID, viewerEmail: viewerEmail,
		inviteToken: waitInvitation,
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(*outputDir) == "" {
		*outputDir = filepath.Join(workspace, ".artifacts", "test-runs", *runID, "multicloud-staging")
	}
	if err := writeMulticloudLiveEvidence(*outputDir, *runID, started, time.Now().UTC(), result); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status": "PASS", "run_id": *runID, "output_dir": *outputDir,
		"workflows":       []string{"WF-MULTICLOUD-LIFECYCLE-001"},
		"workflow_slices": []string{"WF-MULTICLOUD-SHARING-001#cloud-membership"},
	})
}

func multicloudRunScopedViewerEmail(ctx accountManagerContext, runID string) (string, error) {
	store, err := newSecretStore("", firstNonEmpty(ctx.StackValues["CLOUD_ENV_NAME"], "staging"))
	if err != nil {
		return "", err
	}
	operator, err := store.readOperator()
	if err != nil {
		return "", fmt.Errorf("read canonical operator settings: %w", err)
	}
	local, domain, err := loadTestMailboxBase(operator["IMAP_EMAIL_ADDR"])
	if err != nil {
		return "", fmt.Errorf("operator IMAP mailbox: %w", err)
	}
	return local + "+multicloud-" + runID + "@" + domain, nil
}

func runMulticloudLiveScenario(ctx context.Context, api multicloudLiveHTTPClient, in multicloudLiveScenarioInput) (result multicloudLiveScenarioResult, runErr error) {
	for name, value := range map[string]string{
		"run ID": in.runID, "owner token": in.ownerToken, "owner user ID": in.ownerUserID,
		"viewer token": in.viewerToken, "viewer user ID": in.viewerID, "viewer email": in.viewerEmail,
	} {
		if strings.TrimSpace(value) == "" {
			return result, fmt.Errorf("%s is required", name)
		}
	}
	if in.inviteToken == nil {
		return result, errors.New("invitation token waiter is required")
	}
	result.Lifecycle = map[string]string{}
	result.Sharing = map[string]string{}
	cloudName := "RTK Multi-cloud Qualification " + in.runID
	var created map[string]any
	status, err := api.json(ctx, http.MethodPost, "/v1/developer/brand-clouds", in.ownerToken,
		map[string]any{"name": cloudName, "description": "Run-scoped empty-cloud qualification"}, in.runID+"-create", &created)
	if err != nil || status != http.StatusCreated {
		return result, apiStatusError("create developer Brand Cloud", status, err)
	}
	cloud := objectValue(created["brand_cloud"])
	result.CloudID = stringValue(cloud["id"])
	if result.CloudID == "" || stringValue(cloud["owner_user_id"]) != in.ownerUserID || stringValue(cloud["my_role"]) != "owner" {
		return result, errors.New("created Brand Cloud did not bind the exact global owner")
	}
	result.Lifecycle["create_cloud"] = "PASS"
	cleanupNeeded := true
	defer func() {
		if runErr == nil || !cleanupNeeded {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		_, _ = api.json(cleanupCtx, http.MethodDelete, "/v1/developer/brand-clouds/"+url.PathEscape(result.CloudID), in.ownerToken, nil, in.runID+"-failure-cleanup", nil)
	}()

	var owned map[string]any
	status, err = api.json(ctx, http.MethodGet, "/v1/developer/brand-clouds?view=owned&limit=100&offset=0", in.ownerToken, nil, "", &owned)
	if err != nil || status != http.StatusOK || !cloudPageContains(owned, result.CloudID, "owner") {
		return result, apiStatusError("list owner clouds", status, err)
	}
	result.OwnedCount = int(asFloat(owned["owned_count"]))
	result.OwnedLimit = int(asFloat(owned["owned_limit"]))
	if result.OwnedCount < 2 || result.OwnedCount > result.OwnedLimit {
		return result, fmt.Errorf("owned quota evidence is invalid: count=%d limit=%d", result.OwnedCount, result.OwnedLimit)
	}
	result.Lifecycle["list_owned"] = "PASS"

	var updated map[string]any
	status, err = api.json(ctx, http.MethodPatch, "/v1/developer/brand-clouds/"+url.PathEscape(result.CloudID), in.ownerToken,
		map[string]any{"name": cloudName + " Updated", "description": "Renamed without changing UUID or tenant slug"}, in.runID+"-rename", &updated)
	if err != nil || status != http.StatusOK || stringValue(objectValue(updated["brand_cloud"])["id"]) != result.CloudID {
		return result, apiStatusError("rename developer Brand Cloud", status, err)
	}
	result.Lifecycle["rename_cloud"] = "PASS"

	var invite map[string]any
	status, err = api.json(ctx, http.MethodPost, "/v1/developer/brand-clouds/"+url.PathEscape(result.CloudID)+"/members/invitations", in.ownerToken,
		map[string]any{"email": in.viewerEmail, "role": "viewer", "access_scope": map[string]any{"kind": "all_products"}}, in.runID+"-invite", &invite)
	if err != nil || status != http.StatusAccepted {
		return result, apiStatusError("invite viewer", status, err)
	}
	invitation := objectValue(invite["invitation"])
	if stringValue(invitation["status"]) != "pending" || stringValue(invitation["target_user_id"]) != in.viewerID || stringValue(objectValue(invitation["access_scope"])["kind"]) != "all_products" {
		return result, errors.New("pending viewer invitation is not bound to the expected global user and scope")
	}
	result.Sharing["invite_viewer"] = "PASS"
	invitationToken, err := in.inviteToken()
	if err != nil {
		return result, fmt.Errorf("receive viewer invitation: %w", err)
	}
	var accepted map[string]any
	status, err = api.json(ctx, http.MethodPost, "/v1/developer/brand-cloud-member-invitations/accept", in.viewerToken,
		map[string]any{"token": invitationToken}, in.runID+"-accept", &accepted)
	if err != nil || status != http.StatusOK || stringValue(objectValue(accepted["member"])["role"]) != "viewer" {
		return result, apiStatusError("accept viewer invitation", status, err)
	}
	result.Sharing["accept_viewer"] = "PASS"

	var shared map[string]any
	status, err = api.json(ctx, http.MethodGet, "/v1/developer/brand-clouds?view=shared&limit=100&offset=0", in.viewerToken, nil, "", &shared)
	if err != nil || status != http.StatusOK || !cloudPageContains(shared, result.CloudID, "viewer") {
		return result, apiStatusError("list shared clouds", status, err)
	}
	result.Sharing["read_shared_cloud"] = "PASS"
	status, err = api.json(ctx, http.MethodPatch, "/v1/developer/brand-clouds/"+url.PathEscape(result.CloudID), in.viewerToken,
		map[string]any{"description": "viewer must never write"}, in.runID+"-viewer-denied", nil)
	if err != nil || status != http.StatusForbidden {
		return result, fmt.Errorf("viewer cloud mutation was not rejected: HTTP %d: %w", status, err)
	}
	result.Sharing["deny_viewer_write"] = "PASS"

	status, err = api.json(ctx, http.MethodDelete, "/v1/developer/brand-clouds/"+url.PathEscape(result.CloudID)+"/members/"+url.PathEscape(in.viewerID), in.ownerToken, nil, in.runID+"-revoke", nil)
	if err != nil || status != http.StatusNoContent {
		return result, apiStatusError("revoke viewer", status, err)
	}
	result.Sharing["revoke_membership"] = "PASS"
	status, err = api.json(ctx, http.MethodGet, "/v1/developer/brand-clouds/"+url.PathEscape(result.CloudID), in.viewerToken, nil, "", nil)
	if err != nil || status != http.StatusNotFound {
		return result, fmt.Errorf("revoked viewer retained cloud access: HTTP %d: %w", status, err)
	}
	result.Sharing["deny_revoked_cloud_read"] = "PASS"

	var preflight map[string]any
	status, err = api.json(ctx, http.MethodGet, "/v1/developer/brand-clouds/"+url.PathEscape(result.CloudID)+"/deletion-preflight", in.ownerToken, nil, "", &preflight)
	if err != nil || status != http.StatusOK || preflight["eligible"] != true || len(anySlice(preflight["blockers"])) != 0 {
		return result, apiStatusError("preflight empty-cloud deletion", status, err)
	}
	result.Lifecycle["check_deletion"] = "PASS"
	var deletion map[string]any
	status, err = api.json(ctx, http.MethodDelete, "/v1/developer/brand-clouds/"+url.PathEscape(result.CloudID), in.ownerToken, nil, in.runID+"-delete", &deletion)
	if err != nil || status != http.StatusAccepted {
		return result, apiStatusError("request empty-cloud deletion", status, err)
	}
	operation := objectValue(deletion["operation"])
	result.OperationID = stringValue(operation["id"])
	if result.OperationID == "" {
		return result, errors.New("cloud deletion response did not include operation.id")
	}
	result.Lifecycle["request_deletion"] = "PASS"
	for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(time.Second) {
		var observed map[string]any
		status, err = api.json(ctx, http.MethodGet, "/v1/developer/brand-clouds/"+url.PathEscape(result.CloudID)+"/operations/"+url.PathEscape(result.OperationID), in.ownerToken, nil, "", &observed)
		if err != nil || status != http.StatusOK {
			return result, apiStatusError("poll cloud deletion", status, err)
		}
		result.DeletionState = stringValue(objectValue(observed["operation"])["state"])
		switch result.DeletionState {
		case "succeeded":
			cleanupNeeded = false
			result.Lifecycle["await_deletion"] = "PASS"
			return result, nil
		case "blocked", "canceled":
			return result, fmt.Errorf("cloud deletion reached terminal state %s", result.DeletionState)
		}
		if time.Now().After(deadline) {
			return result, errors.New("cloud deletion did not reach succeeded within two minutes")
		}
	}
}

func (c multicloudLiveHTTPClient) json(ctx context.Context, method, path, token string, payload any, idempotencyKey string, out any) (int, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return 0, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, err
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return response.StatusCode, fmt.Errorf("decode HTTP %d response: %w", response.StatusCode, err)
		}
	}
	return response.StatusCode, nil
}

func multicloudInvitationTokenWaiter(workspace string, ctx accountManagerContext, recipient string) (func() (string, error), error) {
	store, err := newSecretStore("", firstNonEmpty(ctx.StackValues["CLOUD_ENV_NAME"], "staging"))
	if err != nil {
		return nil, err
	}
	operator, err := store.readOperator()
	if err != nil {
		return nil, fmt.Errorf("read canonical operator settings: %w", err)
	}
	childEnv := os.Environ()
	for _, key := range []string{"IMAP_SERVER", "IMAP_EMAIL_ADDR", "IMAP_EMAIL_PASSWORD", "IMAP_EMAIL_PORT", "IMAP_EMAIL_SECURITY", "IMAP_EMAIL_FOLDER"} {
		if strings.TrimSpace(operator[key]) == "" {
			return nil, fmt.Errorf("missing operator IMAP setting: %s", key)
		}
		childEnv = append(childEnv, key+"="+operator[key])
	}
	connectHost, err := resolveIMAPConnectHost(operator["IMAP_CONNECT_HOST"], operator["IMAP_SERVER"], net.LookupHost)
	if err != nil {
		return nil, err
	}
	if connectHost != "" {
		childEnv = append(childEnv, "IMAP_CONNECT_HOST="+connectHost)
	}
	stackEnv, _ := readEnvFile(filepath.Join(ctx.EnvRoot, "env", "stack.env"))
	adminBaseURL, err := loadOwnerAdminBaseURL(stackEnv)
	if err != nil {
		return nil, err
	}
	childEnv = append(childEnv,
		"EMAIL_E2E_SIGNUP_EMAIL="+strings.ToLower(strings.TrimSpace(recipient)),
		"EMAIL_E2E_EXPECTED_FROM=no-reply@realtekconnect.com",
		"EMAIL_E2E_EXPECTED_SUBJECT=Join a Realtek Connect+ Brand Cloud",
		"EMAIL_E2E_EXPECTED_PATH=/brand-cloud-member-invitation/accept",
		"AUTH_TOKEN_BASE_URL="+adminBaseURL,
	)
	helper := filepath.Join(workspace, "repos", "rtk_account_manager", "scripts", "email_signup_imap.py")
	snapshot, err := runIMAPJSON(helper, childEnv, "snapshot")
	if err != nil {
		return nil, err
	}
	uidStart := int(asFloat(snapshot["uid_next"]))
	if uidStart < 1 {
		return nil, errors.New("IMAP snapshot did not return a valid UIDNEXT")
	}
	return func() (string, error) {
		delivered, err := runIMAPJSON(helper, childEnv, "wait", "--uid-start", fmt.Sprint(uidStart), "--timeout", firstNonEmpty(os.Getenv("MULTICLOUD_IMAP_TIMEOUT"), "180"))
		if err != nil {
			return "", err
		}
		parsed, err := url.Parse(stringValue(delivered["url"]))
		if err != nil || parsed.Query().Get("token") == "" {
			return "", errors.New("membership invitation email did not contain a valid token URL")
		}
		return parsed.Query().Get("token"), nil
	}, nil
}

func resolveIMAPConnectHost(configured, server string, lookup func(string) ([]string, error)) (string, error) {
	if configured = strings.TrimSpace(configured); configured != "" {
		return configured, nil
	}
	if _, err := lookup(strings.TrimSpace(server)); err == nil {
		return "", nil
	}
	const fallback = "sm.realtekconnect.com"
	if _, err := lookup(fallback); err != nil {
		return "", errors.New("IMAP server DNS failed and no safe connect host is available")
	}
	return fallback, nil
}

func writeMulticloudLiveEvidence(outputDir, runID string, started, completed time.Time, result multicloudLiveScenarioResult) error {
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return err
	}
	evidence := multicloudLiveEvidence{
		Schema: "rtk.multicloud-staging-qualification.v1", RunID: runID, Environment: "staging", Status: "PASS",
		StartedAt: started.Format(time.RFC3339), CompletedAt: completed.Format(time.RFC3339),
		Workflows: map[string]map[string]string{"WF-MULTICLOUD-LIFECYCLE-001": result.Lifecycle},
		WorkflowSlices: map[string]map[string]string{
			"WF-MULTICLOUD-SHARING-001#cloud-membership": result.Sharing,
		},
		Facts: map[string]any{"same_global_owner_created_second_cloud": true, "owned_count_after_create": result.OwnedCount, "owned_limit": result.OwnedLimit, "viewer_scope": "all_products", "viewer_write_denied": true, "revoked_access_denied": true, "empty_cloud_soft_deleted": true},
	}
	if err := writeJSON(filepath.Join(outputDir, "evidence.json"), evidence); err != nil {
		return err
	}
	report := fmt.Sprintf("# Multi-cloud Staging Qualification\n\n- Run ID: `%s`\n- Status: **PASS**\n- Same global owner created a second cloud: **PASS**\n- Viewer invitation, acceptance, write denial, and revocation: **PASS**\n- Empty-cloud preflight and soft deletion: **PASS**\n", runID)
	return os.WriteFile(filepath.Join(outputDir, "report.md"), []byte(report), 0o644)
}

func cloudPageContains(page map[string]any, cloudID, role string) bool {
	for _, item := range anySlice(page["brand_clouds"]) {
		cloud := objectValue(item)
		if stringValue(cloud["id"]) == cloudID && stringValue(cloud["my_role"]) == role {
			return true
		}
	}
	return false
}

func objectValue(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func apiStatusError(operation string, status int, err error) error {
	if err != nil {
		return fmt.Errorf("%s: HTTP %d: %w", operation, status, err)
	}
	return fmt.Errorf("%s: HTTP %d", operation, status)
}
