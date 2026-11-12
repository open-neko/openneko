package webfront

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
)

// API answers the routes that moved from Node to Go. A route it does not
// hold goes to Node.
type API struct {
	Gate    *Gate
	DB      *pgxpool.Pool
	Version string
	// PublicURL overrides OPENNEKO_PUBLIC_URL; tests set it.
	PublicURL string
	// AuditWebhook overrides OPENNEKO_AUDIT_FAILURE_WEBHOOK_URL.
	AuditWebhook string
	// SecureCookies marks the session and sign-in state cookies Secure.
	SecureCookies bool
	// SecretsDir overrides the config folder that holds secrets.json.
	SecretsDir string
	// BossSchema overrides the pg-boss schema, pgboss; tests set it.
	BossSchema string
	// DBRole is the role that a password change rotates; "" means neko.
	DBRole string
	// Restart stops the web process after a password change when
	// OPENNEKO_RESTART_WEB_ON_PASSWORD_CHANGE is 1.
	Restart func()

	once   sync.Once
	routes map[string]map[string]http.HandlerFunc
	orgMu  sync.Mutex
	queues sync.Map
	orgID  string

	hookWaiters atomic.Int64
}

// GoRoutes lists the /api/v1 routes that Go answers, by method.
// TestGoRoutesInContract checks each one against api/openapi.yaml, and
// apps/web/test/api/openapi-contract.test.ts reads this map as text.
var GoRoutes = map[string][]string{
	"/api/v1/version":                                             {"GET"},
	"/api/v1/auth/status":                                         {"GET"},
	"/api/v1/auth/session":                                        {"GET"},
	"/api/v1/auth/begin":                                          {"GET"},
	"/api/v1/auth/callback":                                       {"GET"},
	"/api/v1/auth/logout":                                         {"POST"},
	"/api/v1/auth/device/code":                                    {"POST"},
	"/api/v1/auth/device/token":                                   {"POST"},
	"/api/v1/auth/device/approve":                                 {"POST"},
	"/api/v1/sso/setup/status":                                    {"GET"},
	"/api/v1/sso/setup/environments":                              {"GET"},
	"/api/v1/sso/setup/organizations":                             {"GET"},
	"/api/v1/sso/setup/credentials-info":                          {"GET"},
	"/api/v1/sso/setup/ids":                                       {"POST"},
	"/api/v1/sso/setup/credentials":                               {"POST"},
	"/api/v1/sso/setup/generate-portal-link":                      {"POST"},
	"/api/v1/sso/setup/check":                                     {"POST"},
	"/api/v1/profile":                                             {"GET", "PATCH"},
	"/api/v1/my/pack-accounts/{packId}/{connectionKey}":           {"POST", "DELETE"},
	"/api/v1/observations":                                        {"GET"},
	"/api/v1/approvals":                                           {"GET"},
	"/api/v1/action-requests":                                     {"GET"},
	"/api/v1/action-requests/{actionRequestId}":                   {"GET", "PATCH"},
	"/api/v1/insights/ensure":                                     {"POST"},
	"/api/v1/work/memories":                                       {"GET"},
	"/api/v1/work/memories/decide":                                {"POST"},
	"/api/v1/work/memories/pending":                               {"GET"},
	"/api/v1/work/memories/pull":                                  {"GET", "POST"},
	"/api/v1/work/memories/{id}":                                  {"GET", "PATCH", "DELETE"},
	"/api/v1/work/memories/{id}/promote":                          {"POST"},
	"/api/v1/work/skills":                                         {"GET", "POST"},
	"/api/v1/work/skills/{name}":                                  {"GET", "DELETE"},
	"/api/v1/work/skills/{name}/file":                             {"GET", "PUT"},
	"/api/v1/work/config/history":                                 {"GET"},
	"/api/v1/work/config/restore":                                 {"POST"},
	"/api/v1/work/assets":                                         {"GET"},
	"/api/v1/work/audit/export":                                   {"GET"},
	"/api/v1/work/files/{path}":                                   {"GET"},
	"/api/v1/work/upload":                                         {"POST"},
	"/api/v1/library":                                             {"GET"},
	"/api/v1/library/upload":                                      {"POST"},
	"/api/v1/library/documents/{id}":                              {"GET", "DELETE"},
	"/api/v1/work/threads":                                        {"GET", "POST"},
	"/api/v1/work/threads/{threadId}":                             {"GET", "DELETE"},
	"/api/v1/work/threads/{threadId}/truncate":                    {"POST"},
	"/api/v1/work/threads/{threadId}/runs/{runId}/timing":         {"POST"},
	"/api/v1/work/threads/{threadId}/runs/{runId}/approve-action": {"POST"},
	"/api/v1/work/runs/{runId}/events":                            {"GET"},
	"/api/v1/workflows":                                           {"GET"},
	"/api/v1/workflows/{workflowId}":                              {"GET", "PATCH", "DELETE"},
	"/api/v1/workflows/{workflowId}/adopt":                        {"POST"},
	"/api/v1/workflows/{workflowId}/api-access":                   {"GET", "POST", "PATCH", "DELETE"},
	"/api/v1/workflows/{workflowId}/subscriptions":                {"GET", "POST"},
	"/api/v1/workflows/{workflowId}/subscriptions/dry-run":        {"POST"},
	"/api/v1/subscriptions/{subscriptionId}":                      {"PATCH", "DELETE"},
	"/api/v1/workflow-runs":                                       {"GET"},
	"/api/v1/hooks/{workflowId}":                                  {"POST"},
	"/api/v1/hooks/{workflowId}/result/{runId}":                   {"GET"},
	"/api/v1/workflow-runs/{workflowRunId}":                       {"GET"},
	"/api/v1/workflow-runs/{workflowRunId}/artifact":              {"GET"},
	"/api/v1/workflows/{workflowId}/runs":                         {"POST"},
	"/api/v1/workflows/{workflowId}/runs/{runId}":                 {"GET"},
	"/api/v1/workflows/{workflowId}/runs/{runId}/artifact":        {"GET"},
	"/api/v1/settings/channel-identities":                         {"GET"},
	"/api/v1/settings/channel-identities/{id}":                    {"PATCH"},
	"/api/v1/settings/data-sources":                               {"GET", "PUT"},
	"/api/v1/settings/source-secrets":                             {"GET", "PUT", "DELETE"},
	"/api/v1/settings/install-policy":                             {"GET", "PATCH"},
	"/api/v1/settings/skill-learn":                                {"GET", "PATCH"},
	"/api/v1/settings/graphjin-config":                            {"GET", "PATCH"},
	"/api/v1/settings/persona":                                    {"GET", "PUT"},
	"/api/v1/settings/source-check":                               {"POST"},
	"/api/v1/settings/file-sources":                               {"POST"},
	"/api/v1/settings/openapi-specs":                              {"GET", "POST"},
	"/api/v1/policies":                                            {"GET"},
	"/api/v1/onboarding/status":                                   {"GET"},
	"/api/v1/policies/{policyId}":                                 {"GET", "PATCH"},
	"/api/v1/admin/directory":                                     {"GET", "POST"},
	"/api/v1/admin/groups":                                        {"GET", "POST"},
	"/api/v1/admin/groups/{groupId}":                              {"GET", "PATCH", "DELETE"},
	"/api/v1/admin/groups/{groupId}/members":                      {"POST", "DELETE"},
	"/api/v1/admin/idp-rules":                                     {"GET", "POST"},
	"/api/v1/admin/idp-rules/{ruleId}":                            {"DELETE"},
	"/api/v1/admin/items":                                         {"GET"},
	"/api/v1/admin/item-grants":                                   {"GET", "POST", "DELETE"},
	"/api/v1/admin/data-access":                                   {"GET", "POST"},
	"/api/v1/admin/data-access/settings":                          {"GET", "PATCH"},
	"/api/v1/admin/data-access/tables":                            {"GET"},
	"/api/v1/admin/data-access/{ruleId}":                          {"DELETE"},
	"/api/v1/admin/users":                                         {"POST"},
	"/api/v1/admin/users/{userId}":                                {"PATCH"},
	"/api/v1/admin/users/{userId}/access":                         {"GET"},
	"/api/v1/admin/change-password":                               {"GET", "POST"},
	"/api/v1/admin/devices":                                       {"GET"},
	"/api/v1/admin/devices/{deviceId}":                            {"DELETE"},
	"/api/v1/admin/signin/status":                                 {"GET"},
	"/api/v1/admin/signin/provider":                               {"POST"},
	"/api/v1/admin/signin/email-provider":                         {"POST"},
	"/api/v1/admin/signin/test":                                   {"POST"},
	"/api/v1/admin/public-url":                                    {"GET", "PUT"},
	"/api/v1/admin/channels/settings":                             {"GET", "POST"},
	"/api/v1/admin/spend":                                         {"GET", "PUT"},
	"/api/v1/admin/spend/alerts/{alertId}":                        {"DELETE"},
	"/api/v1/admin/spend/workflows/{workflowId}":                  {"PUT", "DELETE"},
	"/api/v1/admin/workflow-api-limits":                           {"GET", "PUT"},
	"/api/v1/admin/models":                                        {"GET", "POST"},
	"/api/v1/admin/models/{modelId}":                              {"PUT", "DELETE"},
	"/api/v1/admin/model-routes":                                  {"PUT"},
	"/api/v1/admin/packs":                                         {"GET"},
	"/api/v1/admin/packs/upload":                                  {"POST"},
	"/api/v1/admin/packs/{packId}/{action}":                       {"GET", "POST"},
	"/api/v1/admin/packs/magento/store-management":                {"GET", "POST"},
	"/api/v1/admin/reckon-webhooks":                               {"GET", "POST"},
	"/api/v1/admin/reckon-webhooks/{reckonWorkflowId}":            {"DELETE"},
	"/api/v1/admin/context-remote":                                {"GET", "PUT", "DELETE"},
	"/api/v1/admin/context-remote/host-key":                       {"POST"},
	"/api/v1/admin/context-remote/publish":                        {"GET", "POST"},
	"/api/v1/admin/context-remote/updates":                        {"GET", "POST"},
	"/api/v1/bots":                                                {"GET", "POST"},
	"/api/v1/bots/{botId}":                                        {"GET", "PATCH", "DELETE"},
	"/api/v1/bots/{botId}/routines/{workflowId}":                  {"GET", "PATCH"},
	"/api/v1/bots/thread/{threadId}":                              {"GET"},
	"/api/v1/bots/thread/{threadId}/seen":                         {"POST"},
	"/api/v1/integrations/list":                                   {"GET"},
	"/api/v1/integrations/disconnect/{plugin}":                    {"POST"},
	"/api/v1/integrations/connect/{plugin}/start":                 {"GET"},
	"/api/v1/integrations/connect/{plugin}/callback":              {"GET"},
	"/api/v1/pack-accounts/{packId}/{connectionKey}":              {"GET", "POST", "DELETE"},
	"/api/v1/pack-accounts/{packId}/{connectionKey}/start":        {"POST"},
	"/api/v1/pack-accounts/{packId}/{connectionKey}/callback":     {"GET"},
}

// In a catchAll route the last {name} takes the rest of the path, as a Next
// [...name] route does.
var catchAll = map[string]bool{"/api/v1/work/files/{path}": true}

func (a *API) table() map[string]map[string]http.HandlerFunc {
	a.once.Do(func() {
		a.routes = map[string]map[string]http.HandlerFunc{
			"/api/v1/version":                                             {"GET": a.version},
			"/api/v1/auth/status":                                         {"GET": a.authStatus},
			"/api/v1/auth/session":                                        {"GET": a.authSession},
			"/api/v1/auth/begin":                                          {"GET": a.authBegin},
			"/api/v1/auth/callback":                                       {"GET": a.authCallback},
			"/api/v1/auth/logout":                                         {"POST": a.logout},
			"/api/v1/auth/device/code":                                    {"POST": a.deviceCode},
			"/api/v1/auth/device/token":                                   {"POST": a.deviceToken},
			"/api/v1/auth/device/approve":                                 {"POST": a.deviceApprove},
			"/api/v1/sso/setup/status":                                    {"GET": a.ssoSetupStatus},
			"/api/v1/sso/setup/environments":                              {"GET": a.ssoSetupEnvironments},
			"/api/v1/sso/setup/organizations":                             {"GET": a.ssoSetupOrganizations},
			"/api/v1/sso/setup/credentials-info":                          {"GET": a.ssoSetupCredentialsInfo},
			"/api/v1/sso/setup/ids":                                       {"POST": a.ssoSetupIDs},
			"/api/v1/sso/setup/credentials":                               {"POST": a.ssoSetupCredentials},
			"/api/v1/sso/setup/generate-portal-link":                      {"POST": a.ssoSetupPortalLink},
			"/api/v1/sso/setup/check":                                     {"POST": a.ssoSetupCheck},
			"/api/v1/profile":                                             {"GET": a.profileGet, "PATCH": a.profilePatch},
			"/api/v1/my/pack-accounts/{packId}/{connectionKey}":           {"POST": a.personalPackAccount, "DELETE": a.personalPackAccount},
			"/api/v1/observations":                                        {"GET": a.observations},
			"/api/v1/approvals":                                           {"GET": a.approvals},
			"/api/v1/action-requests":                                     {"GET": a.actionRequests},
			"/api/v1/action-requests/{actionRequestId}":                   {"GET": a.actionRequest, "PATCH": a.actionRequestDecision},
			"/api/v1/insights/ensure":                                     {"POST": a.ensureInsights},
			"/api/v1/work/memories":                                       {"GET": a.workMemories},
			"/api/v1/work/memories/decide":                                {"POST": a.decidePendingMemory},
			"/api/v1/work/memories/pending":                               {"GET": a.pendingMemories},
			"/api/v1/work/memories/pull":                                  {"GET": a.memoryPullUpdates, "POST": a.applyMemoryPull},
			"/api/v1/work/memories/{id}":                                  {"GET": a.workMemory, "PATCH": a.personalizeMemory, "DELETE": a.forgetMemory},
			"/api/v1/work/memories/{id}/promote":                          {"POST": a.promoteMemory},
			"/api/v1/work/skills":                                         {"GET": a.workSkills, "POST": a.importSkill},
			"/api/v1/work/skills/{name}":                                  {"GET": a.workSkill, "DELETE": a.deleteSkill},
			"/api/v1/work/skills/{name}/file":                             {"GET": a.skillFileGet, "PUT": a.skillFilePut},
			"/api/v1/work/config/history":                                 {"GET": a.configHistory},
			"/api/v1/work/config/restore":                                 {"POST": a.configRestore},
			"/api/v1/work/assets":                                         {"GET": a.workAssets},
			"/api/v1/work/audit/export":                                   {"GET": a.auditExport},
			"/api/v1/work/files/{path}":                                   {"GET": a.workFile},
			"/api/v1/work/upload":                                         {"POST": a.workUpload},
			"/api/v1/library":                                             {"GET": a.library},
			"/api/v1/library/upload":                                      {"POST": a.libraryUpload},
			"/api/v1/library/documents/{id}":                              {"GET": a.libraryDocumentGet, "DELETE": a.libraryDocumentDelete},
			"/api/v1/work/threads":                                        {"GET": a.workThreads, "POST": a.createThread},
			"/api/v1/work/threads/{threadId}":                             {"GET": a.workThread, "DELETE": a.deleteThread},
			"/api/v1/work/threads/{threadId}/truncate":                    {"POST": a.truncateThread},
			"/api/v1/work/threads/{threadId}/runs/{runId}/timing":         {"POST": a.runTiming},
			"/api/v1/work/threads/{threadId}/runs/{runId}/approve-action": {"POST": a.approveRunAction},
			"/api/v1/work/runs/{runId}/events":                            {"GET": a.workRunEvents},
			"/api/v1/workflows":                                           {"GET": a.workflows},
			"/api/v1/workflows/{workflowId}":                              {"GET": a.workflowDetail, "PATCH": a.workflowPatch, "DELETE": a.workflowDelete},
			"/api/v1/workflows/{workflowId}/adopt":                        {"POST": a.workflowAdopt},
			"/api/v1/workflows/{workflowId}/api-access": {"GET": a.workflowAPIAccessGet, "POST": a.workflowAPIAccessPost,
				"PATCH": a.workflowAPIAccessPatch, "DELETE": a.workflowAPIAccessDelete},
			"/api/v1/workflows/{workflowId}/subscriptions":         {"GET": a.workflowSubscriptions, "POST": a.createSubscription},
			"/api/v1/workflows/{workflowId}/subscriptions/dry-run": {"POST": a.subscriptionDryRun},
			"/api/v1/subscriptions/{subscriptionId}":               {"PATCH": a.subscriptionPatch, "DELETE": a.subscriptionDelete},
			"/api/v1/workflow-runs":                                {"GET": a.workflowRuns},
			"/api/v1/workflow-runs/{workflowRunId}":                {"GET": a.workflowRunDetail},
			"/api/v1/workflow-runs/{workflowRunId}/artifact":       {"GET": a.workflowRunArtifact},
			"/api/v1/hooks/{workflowId}":                           {"POST": a.hookStart},
			"/api/v1/hooks/{workflowId}/result/{runId}":            {"GET": a.hookResult},
			"/api/v1/workflows/{workflowId}/runs":                  {"POST": a.workflowAPIStart},
			"/api/v1/workflows/{workflowId}/runs/{runId}":          {"GET": a.workflowAPIRun},
			"/api/v1/workflows/{workflowId}/runs/{runId}/artifact": {"GET": a.workflowAPIArtifact},
			"/api/v1/settings/channel-identities":                  {"GET": a.channelIdentities},
			"/api/v1/settings/channel-identities/{id}":             {"PATCH": a.channelIdentityPatch},
			"/api/v1/settings/data-sources":                        {"GET": a.dataSources, "PUT": a.dataSourcePut},
			"/api/v1/settings/source-secrets":                      {"GET": a.sourceSecrets, "PUT": a.sourceSecretPut, "DELETE": a.sourceSecretDelete},
			"/api/v1/settings/install-policy":                      {"GET": a.installPolicyGet, "PATCH": a.installPolicyPatch},
			"/api/v1/settings/skill-learn":                         {"GET": a.skillLearnGet, "PATCH": a.skillLearnPatch},
			"/api/v1/settings/graphjin-config":                     {"GET": a.graphjinConfigGet, "PATCH": a.graphjinConfigPatch},
			"/api/v1/settings/persona":                             {"GET": a.personaGet, "PUT": a.personaPut},
			"/api/v1/settings/source-check":                        {"POST": a.sourceCheck},
			"/api/v1/settings/file-sources":                        {"POST": a.fileSourceUpload},
			"/api/v1/settings/openapi-specs":                       {"GET": a.openAPISpecList, "POST": a.openAPISpecImport},
			"/api/v1/policies":                                     {"GET": a.actionPolicies},
			"/api/v1/onboarding/status":                            {"GET": a.onboardingStatus},
			"/api/v1/policies/{policyId}":                          {"GET": a.actionPolicyGet, "PATCH": a.actionPolicyPatch},
			"/api/v1/admin/directory":                              {"GET": a.directoryStatus, "POST": a.directorySync},
			"/api/v1/admin/groups":                                 {"GET": a.adminGroups, "POST": a.adminGroupCreate},
			"/api/v1/admin/groups/{groupId}":                       {"GET": a.adminGroup, "PATCH": a.adminGroupPatch, "DELETE": a.adminGroupDelete},
			"/api/v1/admin/groups/{groupId}/members":               {"POST": a.adminGroupMember, "DELETE": a.adminGroupMember},
			"/api/v1/admin/idp-rules":                              {"GET": a.adminIdpRules, "POST": a.adminIdpRuleCreate},
			"/api/v1/admin/idp-rules/{ruleId}":                     {"DELETE": a.adminIdpRuleDelete},
			"/api/v1/admin/items":                                  {"GET": a.adminItems},
			"/api/v1/admin/item-grants":                            {"GET": a.adminItemHolders, "POST": a.adminItemGrant, "DELETE": a.adminItemGrant},
			"/api/v1/admin/data-access":                            {"GET": a.adminDataAccess, "POST": a.adminDataAccessCreate},
			"/api/v1/admin/data-access/settings":                   {"GET": a.adminDataAccessSettings, "PATCH": a.adminDataAccessSettingsPatch},
			"/api/v1/admin/data-access/tables":                     {"GET": a.adminDataAccessTables},
			"/api/v1/admin/data-access/{ruleId}":                   {"DELETE": a.adminDataAccessDelete},
			"/api/v1/admin/users":                                  {"POST": a.adminUserCreate},
			"/api/v1/admin/users/{userId}":                         {"PATCH": a.adminUserPatch},
			"/api/v1/admin/users/{userId}/access":                  {"GET": a.adminUserAccess},
			"/api/v1/admin/change-password":                        {"GET": a.passwordChanged, "POST": a.changePassword},
			"/api/v1/admin/devices":                                {"GET": a.adminDevices},
			"/api/v1/admin/devices/{deviceId}":                     {"DELETE": a.adminDeviceRevoke},
			"/api/v1/admin/signin/status":                          {"GET": a.adminSigninStatus},
			"/api/v1/admin/signin/provider":                        {"POST": a.adminSigninProvider},
			"/api/v1/admin/signin/email-provider":                  {"POST": a.adminSigninEmailProvider},
			"/api/v1/admin/signin/test":                            {"POST": a.adminSigninTest},
			"/api/v1/admin/public-url":                             {"GET": a.adminPublicURL, "PUT": a.adminPublicURLSave},
			"/api/v1/admin/channels/settings":                      {"GET": a.adminChannelSettings, "POST": a.adminChannelSettings},
			"/api/v1/admin/spend":                                  {"GET": a.adminSpend, "PUT": a.adminSpendSave},
			"/api/v1/admin/spend/alerts/{alertId}":                 {"DELETE": a.adminSpendAlertAck},
			"/api/v1/admin/spend/workflows/{workflowId}":           {"PUT": a.adminWorkflowSpend, "DELETE": a.adminWorkflowSpend},
			"/api/v1/admin/workflow-api-limits":                    {"GET": a.adminWorkflowAPILimits, "PUT": a.adminWorkflowAPILimitsSave},
			"/api/v1/admin/models":                                 {"GET": a.adminModels, "POST": a.adminModelSave},
			"/api/v1/admin/models/{modelId}":                       {"PUT": a.adminModelSave, "DELETE": a.adminModelDelete},
			"/api/v1/admin/model-routes":                           {"PUT": a.adminModelRoute},
			"/api/v1/admin/packs":                                  {"GET": a.adminPacks},
			"/api/v1/admin/packs/upload":                           {"POST": a.adminPackUpload},
			"/api/v1/admin/packs/{packId}/{action}":                {"GET": a.adminPackRead, "POST": a.adminPackWrite},
			"/api/v1/admin/packs/magento/store-management":         {"GET": a.adminStoreManagement, "POST": a.adminStoreManagement},
			"/api/v1/admin/reckon-webhooks":                        {"GET": a.adminCompatWebhooks, "POST": a.adminCompatWebhookImport},
			"/api/v1/admin/reckon-webhooks/{reckonWorkflowId}":     {"DELETE": a.adminCompatWebhookRemove},
			"/api/v1/admin/context-remote":                         {"GET": a.contextRemoteGet, "PUT": a.contextRemotePut, "DELETE": a.contextRemoteDelete},
			"/api/v1/admin/context-remote/host-key":                {"POST": a.contextRemoteHostKey},
			"/api/v1/admin/context-remote/publish":                 {"GET": a.contextRemotePreview, "POST": a.contextRemotePublish},
			"/api/v1/admin/context-remote/updates":                 {"GET": a.contextRemoteUpdates, "POST": a.contextRemoteApply},
			"/api/v1/bots":                                         {"GET": a.bots, "POST": a.botCreate},
			"/api/v1/bots/{botId}":                                 {"GET": a.botDetail, "PATCH": a.botPatch, "DELETE": a.botDelete},
			"/api/v1/bots/{botId}/routines/{workflowId}":           {"GET": a.botRoutineRunsPage, "PATCH": a.botRoutineEnable},
			"/api/v1/bots/thread/{threadId}":                       {"GET": a.botByThread},
			"/api/v1/bots/thread/{threadId}/seen":                  {"POST": a.botSeen},
			"/api/v1/integrations/list":                            {"GET": a.integrationsList},
			"/api/v1/integrations/disconnect/{plugin}":             {"POST": a.integrationDisconnect},
			"/api/v1/integrations/connect/{plugin}/start":          {"GET": a.integrationConnectStart},
			"/api/v1/integrations/connect/{plugin}/callback":       {"GET": a.integrationConnectCallback},
			"/api/v1/pack-accounts/{packId}/{connectionKey}": {"GET": a.packOAuthStatus, "POST": a.packOAuthConfigure,
				"DELETE": a.packOAuthDisconnect},
			"/api/v1/pack-accounts/{packId}/{connectionKey}/start":    {"POST": a.packOAuthStart},
			"/api/v1/pack-accounts/{packId}/{connectionKey}/callback": {"GET": a.packOAuthCallback},
		}
	})
	return a.routes
}

// Handler returns the Go handler for the request, or nil to pass it to Node.
// A method with a nil handler in the table stays with Node.
func (a *API) Handler(r *http.Request) http.HandlerFunc {
	if a == nil {
		return nil
	}
	methods, ok := a.table()[APIV1Path(r.URL.Path)]
	if !ok {
		methods = a.templateMatch(r)
		if methods == nil {
			return nil
		}
	}
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	if h, ok := methods[method]; ok {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		allow := make([]string, 0, len(methods))
		for m := range methods {
			allow = append(allow, m)
		}
		sort.Strings(allow)
		w.Header().Set("Allow", strings.Join(allow, ", "))
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// templateMatch finds a route with {name} segments and sets the path values,
// decoded per segment as Next does.
func (a *API) templateMatch(r *http.Request) map[string]http.HandlerFunc {
	got := strings.Split(APIV1Path(r.URL.EscapedPath()), "/")
	for route, methods := range a.table() {
		if !strings.Contains(route, "{") {
			continue
		}
		want := strings.Split(route, "/")
		if len(want) != len(got) && !(catchAll[route] && len(got) > len(want)) {
			continue
		}
		values := map[string]string{}
		matched := true
		for i, seg := range want {
			if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
				rest := got[i : i+1]
				if catchAll[route] && i == len(want)-1 {
					rest = got[i:]
				}
				decoded := make([]string, len(rest))
				for j, raw := range rest {
					v, err := url.PathUnescape(raw)
					if err != nil || v == "" {
						matched = false
						break
					}
					decoded[j] = v
				}
				if !matched {
					break
				}
				values[seg[1:len(seg)-1]] = strings.Join(decoded, "/")
			} else if seg != got[i] {
				matched = false
				break
			}
		}
		if matched {
			for k, v := range values {
				r.SetPathValue(k, v)
			}
			return methods
		}
	}
	return nil
}

func sendJSON(w http.ResponseWriter, status int, body any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimRight(buf.Bytes(), "\n"))
}

func sendError(w http.ResponseWriter, status int, message string) {
	sendJSON(w, status, map[string]string{"error": message})
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	sendError(w, http.StatusInternalServerError, "internal server error")
}

const maxBody = 1 << 20

// readBody parses a JSON body as Node's `request.json().catch(...)` does:
// a body that is not JSON gives ok false.
func readBody(r *http.Request) (any, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	return v, true
}

func field(body any, name string) (any, bool) {
	m, ok := body.(map[string]any)
	if !ok {
		return nil, false
	}
	v, ok := m[name]
	return v, ok
}

func (a *API) publicBaseURL(r *http.Request) string {
	configured := a.PublicURL
	if configured == "" {
		configured = os.Getenv("OPENNEKO_PUBLIC_URL")
	}
	if c := strings.TrimRight(strings.TrimSpace(configured), "/"); c != "" {
		return c
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

var errNoOrg = errors.New("organization not found")
