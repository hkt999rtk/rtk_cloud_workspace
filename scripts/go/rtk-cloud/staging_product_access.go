package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var stagingProductAccessUUIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

type stagingProductAccessGrant struct {
	UserID    string
	ProductID string
}

func runGrantStagingProductAccess(args []string) error {
	fs := flag.NewFlagSet("grant-staging-product-access", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "RTK", "brand name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*envRootFlag) == "" {
		return errors.New("--env-root is required")
	}
	workspace := strings.TrimSpace(*workspaceFlag)
	if workspace == "" {
		var err error
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	stackEnv, _ := readEnvFile(envRoot + "/env/stack.env")
	if firstNonEmpty(stackEnv["CLOUD_ENV_NAME"], os.Getenv("CLOUD_ENV_NAME")) != "staging" {
		return errors.New("grant-staging-product-access is restricted to the staging environment")
	}
	store, err := openTestDataStore(envRoot, *brandname)
	if err != nil {
		return err
	}
	defer store.Close()
	artifact, err := store.ReadBindArtifact(*brandname)
	if err != nil {
		return err
	}
	users, _, err := store.ReadUsersList(*brandname)
	if err != nil {
		return err
	}
	grants, err := stagingProductAccessGrants(artifact.Assignments, users)
	if err != nil {
		return err
	}
	if len(grants) == 0 {
		return errors.New("no bound synthetic user Product access grants were found")
	}
	if !stagingProductAccessUUIDPattern.MatchString(artifact.BrandCloudID) {
		return errors.New("test-data binding artifact has an invalid Brand Cloud ID")
	}
	ctx, err := factoryProductionAccountContext(workspace, envRoot)
	if err != nil {
		return err
	}
	session, err := accountLoginSession(ctx, func(string, ...any) {})
	ctx.Close()
	if err != nil {
		return fmt.Errorf("authenticate staging Product access owner: %w", err)
	}
	if !stagingProductAccessUUIDPattern.MatchString(session.UserID) {
		return errors.New("staging Product access owner has an invalid user ID")
	}
	sql, err := buildStagingProductAccessSQL(artifact.BrandCloudID, session.UserID, grants)
	if err != nil {
		return err
	}
	stack := firstNonEmpty(stackEnv["CLOUD_STACK_NAME"], "video-cloud-staging")
	kubeconfig, err := ensureK8SKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return err
	}
	cmd := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "--request-timeout="+firstNonEmpty(os.Getenv("RTK_CLOUD_KUBECTL_REQUEST_TIMEOUT"), "20s"),
		"-n", stack+"-platform", "exec", "-i", "postgresql-0", "--", "sh", "-ceu",
		`PGPASSWORD="$POSTGRES_PASSWORD" exec psql -X -v ON_ERROR_STOP=1 -U postgres -d video_cloud -q -f -`)
	cmd.Stdin = strings.NewReader(sql)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("grant staging Product access failed: %w: %s", err, truncateForLog(string(output), 300))
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"action":         "granted",
		"brandname":      *brandname,
		"brand_cloud_id": artifact.BrandCloudID,
		"grant_count":    len(grants),
	})
}

func stagingProductAccessGrants(assignments []bindAssignment, users map[string]userCredential) ([]stagingProductAccessGrant, error) {
	unique := map[string]stagingProductAccessGrant{}
	for _, assignment := range assignments {
		user, ok := users[assignment.AssignedEmail]
		if !ok || strings.TrimSpace(user.UserID) == "" {
			return nil, fmt.Errorf("bound user is missing from test-data credentials: %s", assignment.AssignedEmail)
		}
		if strings.TrimSpace(assignment.ProductID) == "" {
			return nil, fmt.Errorf("bound device is missing its Product ID: %s", assignment.DeviceID)
		}
		grant := stagingProductAccessGrant{UserID: user.UserID, ProductID: assignment.ProductID}
		unique[grant.UserID+"\x00"+grant.ProductID] = grant
	}
	out := make([]stagingProductAccessGrant, 0, len(unique))
	for _, grant := range unique {
		out = append(out, grant)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UserID != out[j].UserID {
			return out[i].UserID < out[j].UserID
		}
		return out[i].ProductID < out[j].ProductID
	})
	return out, nil
}

func buildStagingProductAccessSQL(brandCloudID, ownerUserID string, grants []stagingProductAccessGrant) (string, error) {
	for name, value := range map[string]string{"Brand Cloud ID": brandCloudID, "owner user ID": ownerUserID} {
		if !stagingProductAccessUUIDPattern.MatchString(value) {
			return "", fmt.Errorf("invalid %s", name)
		}
	}
	if len(grants) == 0 {
		return "", errors.New("at least one staging Product access grant is required")
	}
	values := make([]string, 0, len(grants))
	for _, grant := range grants {
		if !stagingProductAccessUUIDPattern.MatchString(grant.UserID) || !stagingProductAccessUUIDPattern.MatchString(grant.ProductID) {
			return "", errors.New("staging Product access grant contains an invalid UUID")
		}
		values = append(values, fmt.Sprintf("('%s'::uuid,'%s'::uuid)", strings.ToLower(grant.UserID), strings.ToLower(grant.ProductID)))
	}
	expected := strconv.Itoa(len(grants))
	cloud := strings.ToLower(brandCloudID)
	owner := strings.ToLower(ownerUserID)
	return fmt.Sprintf(`BEGIN;
CREATE TEMP TABLE requested_product_admissions(user_id uuid, product_id uuid, PRIMARY KEY(user_id,product_id)) ON COMMIT DROP;
INSERT INTO requested_product_admissions(user_id,product_id) VALUES %s;
DO $rtk$
DECLARE eligible_count integer;
BEGIN
  IF NOT EXISTS (SELECT 1 FROM organization_members WHERE organization_id='%s'::uuid AND user_id='%s'::uuid AND role='owner') THEN
    RAISE EXCEPTION 'authenticated staging operator is not the Brand Cloud owner';
  END IF;
  SELECT count(*) INTO eligible_count
  FROM requested_product_admissions r
  JOIN organization_members m ON m.organization_id='%s'::uuid AND m.user_id=r.user_id AND m.role IN ('admin','member')
  JOIN device_item_profiles p ON p.id=r.product_id AND p.brand_cloud_id='%s'::uuid;
  IF eligible_count <> %s THEN
    RAISE EXCEPTION 'staging Product access inputs are incomplete or outside the Brand Cloud';
  END IF;
END
$rtk$;
INSERT INTO brand_cloud_product_admissions(organization_id,user_id,product_id,provenance,approved_by)
SELECT '%s'::uuid,r.user_id,r.product_id,'owner_invitation','%s'::uuid
FROM requested_product_admissions r
ON CONFLICT (organization_id,user_id,product_id) DO NOTHING;
DO $rtk$
DECLARE granted_count integer;
BEGIN
  SELECT count(*) INTO granted_count
  FROM requested_product_admissions r
  JOIN brand_cloud_product_admissions a ON a.organization_id='%s'::uuid AND a.user_id=r.user_id AND a.product_id=r.product_id;
  IF granted_count <> %s THEN
    RAISE EXCEPTION 'staging Product access verification failed';
  END IF;
END
$rtk$;
COMMIT;
`, strings.Join(values, ","), cloud, owner, cloud, cloud, expected, cloud, owner, cloud, expected), nil
}
