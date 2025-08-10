admin.go:type AdminPageData struct {
admin.go:func (s *WebServer) getUptime() string {
admin.go:func (s *WebServer) countEnabledAPITokens(tokens []*database.APIToken) int {
admin.go:func (s *WebServer) adminCreateNewsgroup(c *gin.Context) {
admin.go:func (s *WebServer) adminUpdateNewsgroup(c *gin.Context) {
admin.go:func (s *WebServer) adminDeleteNewsgroup(c *gin.Context) {
admin.go:func (s *WebServer) adminCreateProvider(c *gin.Context) {
admin.go:func (s *WebServer) adminUpdateProvider(c *gin.Context) {
admin.go:func (s *WebServer) adminDeleteProvider(c *gin.Context) {
admin.go:func (s *WebServer) adminCreateAPIToken(c *gin.Context) {
admin.go:func (s *WebServer) adminToggleAPIToken(c *gin.Context) {
admin.go:func (s *WebServer) adminDeleteAPIToken(c *gin.Context) {
admin.go:func (s *WebServer) adminCleanupExpiredTokens(c *gin.Context) {
admin_userfuncs.go:func (s *WebServer) countAdminUsers(users []*models.User) int {
admin_userfuncs.go:func (s *WebServer) countActiveSessions() int {
admin_userfuncs.go:func (s *WebServer) adminPage(c *gin.Context) {
admin_userfuncs.go:func (s *WebServer) adminCreateUser(c *gin.Context) {
admin_userfuncs.go:func (s *WebServer) adminUpdateUser(c *gin.Context) {
admin_userfuncs.go:func (s *WebServer) adminDeleteUser(c *gin.Context) {
admin_userfuncs.go:func (s *WebServer) isAdmin(user *models.User) bool {
aichatPage.go:type ChatMessage struct {
aichatPage.go:type AIChatPageData struct {
aichatPage.go:func (s *WebServer) aichatPage(c *gin.Context) {
aichatPage.go:func (s *WebServer) aichatSend(c *gin.Context) {
apiHandlers.go:func (s *WebServer) listGroups(c *gin.Context) {
apiHandlers.go:func (s *WebServer) getGroupOverview(c *gin.Context) {
apiHandlers.go:func (s *WebServer) getArticle(c *gin.Context) {
apiHandlers.go:func (s *WebServer) getArticleByMessageId(c *gin.Context) {
apiHandlers.go:func (s *WebServer) getGroupThreads(c *gin.Context) {
apiHandlers.go:func (s *WebServer) getStats(c *gin.Context) {
apitokens.go:func (s *WebServer) APIAuthRequired() gin.HandlerFunc {
apitokens.go:func (s *WebServer) createAPITokenHandler(c *gin.Context) {
apitokens.go:func (s *WebServer) listAPITokensHandler(c *gin.Context) {
apitokens.go:func (s *WebServer) disableAPITokenHandler(c *gin.Context) {
apitokens.go:func (s *WebServer) enableAPITokenHandler(c *gin.Context) {
apitokens.go:func (s *WebServer) deleteAPITokenHandler(c *gin.Context) {
apitokens.go:func (s *WebServer) cleanupExpiredTokensHandler(c *gin.Context) {
articlePage.go:func (s *WebServer) articlePage(c *gin.Context) {
articlePage.go:func (s *WebServer) articleByMessageIdPage(c *gin.Context) {
auth.go:type AuthUser struct {
auth.go:type SessionData struct {
auth.go:func (s *WebServer) WebAuthRequired() gin.HandlerFunc {
auth.go:func (s *WebServer) WebAdminRequired() gin.HandlerFunc {
auth.go:func (s *WebServer) getWebSession(c *gin.Context) *SessionData {
auth.go:func (s *WebServer) createWebSession(c *gin.Context, userID int64) error {
auth.go:func (s *WebServer) destroyWebSession(c *gin.Context) {
auth.go:func hashPassword(password string) (string, error) {
auth.go:func checkPassword(password, hash string) bool {
auth.go:func validateEmail(email string) bool {
auth.go:func validateUsername(username string) error {
auth.go:func validatePassword(password string) error {
groupPage.go:func (s *WebServer) groupPage(c *gin.Context) {
groupsPage.go:func (s *WebServer) groupsPage(c *gin.Context) {
groupThreadsPage.go:func (s *WebServer) groupThreadsPage(c *gin.Context) {
helpPage.go:func (s *WebServer) helpPage(c *gin.Context) {
homePage.go:func (s *WebServer) homePage(c *gin.Context) {
login.go:type LoginPageData struct {
login.go:func (s *WebServer) loginPage(c *gin.Context) {
login.go:func (s *WebServer) loginSubmit(c *gin.Context) {
login.go:func (s *WebServer) logout(c *gin.Context) {
login.go:func (s *WebServer) renderLoginError(c *gin.Context, errorMsg, redirectURL string) {
profile.go:type ProfilePageData struct {
profile.go:func (s *WebServer) profilePage(c *gin.Context) {
profile.go:func (s *WebServer) profileUpdate(c *gin.Context) {
register.go:type RegisterPageData struct {
register.go:func (s *WebServer) registerPage(c *gin.Context) {
register.go:func (s *WebServer) registerSubmit(c *gin.Context) {
register.go:func (s *WebServer) createUser(username, email, passwordHash, displayName string) (*models.User, error) {
register.go:func (s *WebServer) renderRegisterError(c *gin.Context, errorMsg, username, email string) {
searchPage.go:func (s *WebServer) searchPage(c *gin.Context) {
sectionsPage.go:func (s *WebServer) sectionsPage(c *gin.Context) {
sectionsPage.go:func (s *WebServer) sectionPage(c *gin.Context) {
sectionsPage.go:func (s *WebServer) sectionGroupPage(c *gin.Context) {
sectionsPage.go:func (s *WebServer) sectionArticlePage(c *gin.Context) {
sectionsPage.go:func (s *WebServer) sectionArticleByMessageIdPage(c *gin.Context) {
server_core.go:type WebServer struct {
server_core.go:type TemplateData struct {
server_core.go:type GroupPageData struct {
server_core.go:type ArticlePageData struct {
server_core.go:type StatsPageData struct {
server_core.go:type GroupsPageData struct {
server_core.go:type SectionPageData struct {
server_core.go:type SectionGroupPageData struct {
server_core.go:type SectionArticlePageData struct {
server_core.go:type SearchPageData struct {
server_core.go:func NewServer(db *database.Database, webconfig *config.WebConfig, nntpconfig *nntp.NNTPServer) *WebServer {
server_core.go:func (s *WebServer) setupRoutes() {
server_core.go:func (s *WebServer) Start() error {
statsPage.go:func (s *WebServer) statsPage(c *gin.Context) {
threadPage.go:func (s *WebServer) singleThreadPage(c *gin.Context) {
threadTreePage.go:func (s *WebServer) handleThreadTreeAPI(c *gin.Context) {
threadTreePage.go:func (s *WebServer) threadTreePage(c *gin.Context) {
threadTreePage.go:func (s *WebServer) sectionThreadTreePage(c *gin.Context) {
threadTreePage.go:func (s *WebServer) threadTreeDemoPage(c *gin.Context) {
utils.go:func (s *WebServer) GetPort() int {
utils.go:func (s *WebServer) NNTPGetTCPPort() int {
utils.go:func (s *WebServer) NNTPGetTLSPort() int {
utils.go:func (s *WebServer) getBaseTemplateData(c *gin.Context, title string) TemplateData {
utils.go:func (s *WebServer) isAdminUser(user *models.User) bool {
utils.go:func (s *WebServer) renderError(c *gin.Context, statusCode int, message string, errstring string) {
utils.go:func (s *WebServer) renderTemplate(c *gin.Context, templateName string, data interface{}) {
utils.go:func (s *WebServer) GetGroupCount() int {
utils.go:func referencesAnyInThread(references string, threadMessageIDs map[string]bool) bool {
utils.go:func parseReferences(references string) []string {
