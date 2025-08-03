### Struct Signatures
./active_files/hierarchies/organize_by_theme.go:15:type ThemeCategory struct {
./cmd/extract_hierarchies/extract_hierarchies.go:137:	type hierarchyStat struct {
./cmd/history-rebuild/main.go:25:type RebuildStats struct {
./cmd/history-rebuild/main.go:35:type HistoryAnalysisStats struct {
./cmd/import-flat-files/main.go:192:type FileScanner struct {
./cmd/import-flat-files/main.go:56:type Article struct {
./cmd/import-flat-files/main.go:68:type DBManager struct {
./cmd/merge-active/main.go:14:type ActiveEntry struct {
./cmd/nntp-server/processor_adapter.go:10:type ProcessorAdapter struct {
./cmd/recover-db/main.go:18:type GroupResult struct {
./cmd/recover-db/main.go:589:type DateProblem struct {
./cmd/test-MsgIdItemCache/main.go:305:type WorkerStats struct {
./cmd/web/main.go:73:type ProcessorAdapter struct {
./internal/cache/newsgroup_cache.go:11:type Newsgroup struct {
./internal/cache/newsgroup_cache.go:28:type CachedGroupsResult struct {
./internal/cache/newsgroup_cache.go:37:type NewsgroupCache struct {
./internal/cache/sanitized_cache.go:12:type SanitizedArticle struct {
./internal/cache/sanitized_cache.go:26:type SanitizedCache struct {
./internal/config/config.go:31:type MainConfig struct {
./internal/config/config.go:53:type Provider struct {
./internal/config/config.go:68:type ServerConfig struct {
./internal/config/config.go:84:type DatabaseConfig struct {
./internal/config/config.go:91:type WebConfig struct {
./internal/database/activeDB.go:15:type ActiveDB struct {
./internal/database/article_cache.go:13:type ArticleCacheEntry struct {
./internal/database/article_cache.go:22:type ArticleCache struct {
./internal/database/database.go:255:type Stats struct {
./internal/database/db_apitokens.go:11:type APIToken struct {
./internal/database/db_batch.go:1005:type threadCacheUpdateData struct {
./internal/database/db_batch.go:1010:type BatchOrchestrator struct {
./internal/database/db_batch.go:108:type ArticleWrap struct {
./internal/database/db_batch.go:43:type MsgIdTmpCacheItem struct {
./internal/database/db_batch.go:51:type OverviewBatch struct {
./internal/database/db_batch.go:57:type ThreadCacheBatch struct {
./internal/database/db_batch.go:77:type SQ3batch struct {
./internal/database/db_batch.go:86:type BatchTasks struct {
./internal/database/db_groupdbs.go:17:type GroupDBs struct {
./internal/database/db_init.go:19:type Database struct {
./internal/database/db_init.go:48:type DBConfig struct {
./internal/database/db_migrate.go:45:type MigrationFile struct {
./internal/database/db_rescan.go:314:type Article struct {
./internal/database/db_rescan.go:336:type Newsgroup struct {
./internal/database/db_rescan.go:353:type Overview struct {
./internal/database/db_rescan.go:369:type ForumThread struct {
./internal/database/db_rescan.go:377:type Thread struct {
./internal/database/db_rescan.go:84:type ConsistencyReport struct {
./internal/database/groups_hashmap.go:31:type HashedEntry struct {
./internal/database/groups_hashmap.go:36:type GroupEntry struct {
./internal/database/groups_hashmap.go:41:type GHmap struct {
./internal/database/nntp_auth_cache.go:11:type AuthCacheEntry struct {
./internal/database/nntp_auth_cache.go:19:type NNTPAuthCache struct {
./internal/database/progress.go:16:type ProgressDB struct {
./internal/database/progress.go:21:type ProgressEntry struct {
./internal/database/sanitize_cache.go:10:type SanitizedFields struct {
./internal/database/sanitize_cache.go:5:type SanitizedArticleCache struct {
./internal/database/sections_cache.go:13:type GroupSectionDBCache struct {
./internal/database/thread_cache.go:132:type MemGroupThreadCache struct {
./internal/database/thread_cache.go:142:type MemCachedThreads struct {
./internal/database/thread_cache.go:17:type ThreadCacheEntry struct {
./internal/database/tree_cache.go:18:type TreeNode struct {
./internal/database/tree_cache.go:31:type ThreadTree struct {
./internal/database/tree_cache.go:42:type TreeStats struct {
./internal/database/tree_view_api.go:15:type TreeViewOptions struct {
./internal/database/tree_view_api.go:24:type TreeViewResponse struct {
./internal/fediverse/activitypub.go:14:type ActivityPubServer struct {
./internal/fediverse/activitypub.go:21:type Actor struct {
./internal/fediverse/activitypub.go:35:type PublicKey struct {
./internal/fediverse/activitypub.go:41:type Activity struct {
./internal/fediverse/activitypub.go:50:type Note struct {
./internal/fediverse/activitypub.go:62:type Tag struct {
./internal/fediverse/bridge.go:11:type Bridge struct {
./internal/history/history_config.go:118:type HistoryStats struct {
./internal/history/history_config.go:130:type HistoryConfig struct {
./internal/history/history_config.go:36:type SQLite3Opts struct {
./internal/history/history_config.go:45:type ThreadingInfo struct {
./internal/history/history_config.go:52:type MessageIdItem struct {
./internal/history/history_config.go:75:type History struct {
./internal/history/history_L1-cache.go:12:type L1CacheEntry struct {
./internal/history/history_L1-cache.go:145:type L1CACHE struct {
./internal/history/history_L1-cache.go:154:type L1CACHEMAP struct {
./internal/history/history_L1-cache.go:158:type L1ITEM struct {
./internal/history/history_L1-cache.go:163:type L1ECH struct {
./internal/history/history_L1-cache.go:167:type L1MUXER struct {
./internal/history/history_L1-cache.go:171:type L1pqQ struct {
./internal/history/history_L1-cache.go:179:type L1PQItem struct {
./internal/history/history_L1-cache.go:185:type CCC struct {
./internal/history/history_L1-cache.go:189:type ClearCacheChan struct {
./internal/history/history_L1-cache.go:18:type L1Cache struct {
./internal/history/history_L1-cache.go:193:type ClearCache struct {
./internal/history/history_MsgIdItemCache.go:28:type MsgIdItemCache struct {
./internal/history/history_MsgIdItemCache.go:36:type MsgIdItemCachePage struct {
./internal/history/history sqlite3 normal.go:12:type SQLite3DB struct {
./internal/history/history sqlite3_sharded.go:11:type SQLite3ShardedDB struct {
./internal/history/history sqlite3_sharded.go:23:type ShardConfig struct {
./internal/matrix/bridge.go:11:type Bridge struct {
./internal/matrix/client.go:14:type MatrixClient struct {
./internal/matrix/client.go:21:type MatrixMessage struct {
./internal/matrix/client.go:28:type MatrixEvent struct {
./internal/matrix/client.go:34:type RoomCreateRequest struct {
./internal/matrix/client.go:44:type RoomCreateResponse struct {
./internal/models/cache.go:24:type CacheKey struct {
./internal/models/models.go:105:type UserPermission struct {
./internal/models/models.go:113:type Article struct {
./internal/models/models.go:15:type Hierarchy struct {
./internal/models/models.go:182:type Thread struct {
./internal/models/models.go:192:type ForumThread struct {
./internal/models/models.go:25:type Provider struct {
./internal/models/models.go:309:type PaginatedResponse struct {
./internal/models/models.go:320:type PaginationInfo struct {
./internal/models/models.go:363:type Section struct {
./internal/models/models.go:376:type SectionGroup struct {
./internal/models/models.go:387:type ActiveNewsgroup struct {
./internal/models/models.go:407:type APIToken struct {
./internal/models/models.go:420:type AIModel struct {
./internal/models/models.go:42:type Newsgroup struct {
./internal/models/models.go:434:type NNTPUser struct {
./internal/models/models.go:448:type NNTPSession struct {
./internal/models/models.go:459:type Setting struct {
./internal/models/models.go:465:type SiteNews struct {
./internal/models/models.go:476:type SpamTracking struct {
./internal/models/models.go:64:type Overview struct {
./internal/models/models.go:82:type User struct {
./internal/models/models.go:97:type Session struct {
./internal/nntp/nntp-article-common.go:26:type ArticleRetrievalResult struct {
./internal/nntp/nntp-auth-manager.go:12:type AuthManager struct {
./internal/nntp/nntp-backend-pool.go:15:type Pool struct {
./internal/nntp/nntp-backend-pool.go:281:type PoolStats struct {
./internal/nntp/nntp-cache-local.go:11:type Local430 struct {
./internal/nntp/nntp-cache-local.go:57:type CacheMessageIDNumtoGroup struct {
./internal/nntp/nntp-cache-local.go:62:type ItemCMIDNG struct {
./internal/nntp/nntp-client.go:101:type OverviewLine struct {
./internal/nntp/nntp-client.go:113:type HeaderLine struct {
./internal/nntp/nntp-client.go:52:type BackendConn struct {
./internal/nntp/nntp-client.go:68:type BackendConfig struct {
./internal/nntp/nntp-client.go:83:type Article struct {
./internal/nntp/nntp-client.go:92:type GroupInfo struct {
./internal/nntp/nntp-cmd-posting.go:163:type ArticleData struct {
./internal/nntp/nntp-peering.go:153:type PeeringStats struct {
./internal/nntp/nntp-peering.go:47:type PeeringManager struct {
./internal/nntp/nntp-peering.go:70:type PeeringConfig struct {
./internal/nntp/nntp-peering.go:94:type Peer struct {
./internal/nntp/nntp-peering-pattern.go:8:type PatternMatchResult struct {
./internal/nntp/nntp-server-cliconns.go:18:type ClientConnection struct {
./internal/nntp/nntp-server.go:33:type NNTPServer struct {
./internal/nntp/nntp-server-statistics.go:9:type ServerStats struct {
./internal/processor/analyze.go:165:type AnalyzeOptions struct {
./internal/processor/analyze.go:20:type GroupAnalysis struct {
./internal/processor/analyze.go:36:type DateRangeResult struct {
./internal/processor/analyze.go:46:type ArticleSizeStats struct {
./internal/processor/bridges.go:11:type BridgeConfig struct {
./internal/processor/bridges.go:24:type BridgeManager struct {
./internal/processor/counter.go:12:type Counter struct {
./internal/processor/proc_DLArt.go:14:type BatchQueue struct {
./internal/processor/proc_DLArt.go:35:	type selectResult struct {
./internal/processor/processor.go:126:type batchItem struct {
./internal/processor/processor.go:23:type Processor struct {
./internal/processor/proc_MsgIDtmpCache.go:14://type MsgTmpCache struct {
./internal/processor/proc_MsgIDtmpCache.go:57:type MsgIdTmpCacheItem struct {
./internal/processor/rslight.go:25:type LegacyImporter struct {
./internal/processor/rslight.go:38:type MenuConfEntry struct {
./internal/processor/rslight.go:45:type GroupsEntry struct {
./internal/processor/rslight.go:52:type LegacyArticle struct {
./internal/processor/rslight.go:65:type LegacyThread struct {
./internal/web/web_admin.go:11:type FlashMessage struct {
./internal/web/web_admin.go:17:type SpamArticleInfo struct {
./internal/web/web_admin.go:23:type AdminPageData struct {
./internal/web/web_admin_ollama.go:18:type ProxyModelResponse struct {
./internal/web/web_admin_ollama.go:22:type ProxyModel struct {
./internal/web/web_aichatPage.go:64:type ChatMessage struct {
./internal/web/web_aichatPage.go:70:type AIChatPageData struct {
./internal/web/web_auth.go:53:type AuthUser struct {
./internal/web/web_auth.go:62:type SessionData struct {
./internal/web/web_login.go:13:type LoginPageData struct {
./internal/web/web_newsPage.go:12:type NewsPageData struct {
./internal/web/web_profile.go:13:type ProfilePageData struct {
./internal/web/web_registerPage.go:15:type RegisterPageData struct {
./internal/web/webserver_core_routes.go:103:type HierarchiesPageData struct {
./internal/web/webserver_core_routes.go:111:type HierarchyGroupsPageData struct {
./internal/web/webserver_core_routes.go:120:type SectionPageData struct {
./internal/web/webserver_core_routes.go:130:type SectionGroupPageData struct {
./internal/web/webserver_core_routes.go:140:type SectionArticlePageData struct {
./internal/web/webserver_core_routes.go:152:type SearchPageData struct {
./internal/web/webserver_core_routes.go:24:type WebServer struct {
./internal/web/webserver_core_routes.go:36:type TemplateData struct {
./internal/web/webserver_core_routes.go:53:type GroupPageData struct {
./internal/web/webserver_core_routes.go:61:type ArticlePageData struct {
./internal/web/webserver_core_routes.go:72:type StatsPageData struct {
./internal/web/webserver_core_routes.go:79:type GroupsPageData struct {
./internal/web/webserver_core_routes.go:87:type GroupThreadsPageData struct {
### Function Signatures
./cmd/history-rebuild/main.go:287:func (s *RebuildStats) PrintProgress() {
./cmd/history-rebuild/main.go:318:func (s *RebuildStats) PrintFinal() {
./cmd/import-flat-files/main.go:161:func (dm *DBManager) ArticleExists(article *Article) (bool, error) {
./cmd/import-flat-files/main.go:180:func (dm *DBManager) Close() {
./cmd/import-flat-files/main.go:205:func (fs *FileScanner) ScanFiles() <-chan *Article {
./cmd/import-flat-files/main.go:237:func (fs *FileScanner) scanDirectory(headDir, bodyDir string, articles chan<- *Article) error {
./cmd/import-flat-files/main.go:79:func (dm *DBManager) GetDB(dbName string) (*sql.DB, error) {
./cmd/nntp-server/processor_adapter.go:20:func (pa *ProcessorAdapter) ProcessIncomingArticle(article *models.Article) (int, error) {
./cmd/nntp-server/processor_adapter.go:27:func (pa *ProcessorAdapter) Lookup(msgIdItem *history.MessageIdItem) (int, error) {
./cmd/nntp-server/processor_adapter.go:32:func (pa *ProcessorAdapter) CheckNoMoreWorkInHistory() bool {
./cmd/web/main.go:83:func (pa *ProcessorAdapter) ProcessIncomingArticle(article *models.Article) (int, error) {
./cmd/web/main.go:90:func (pa *ProcessorAdapter) Lookup(msgIdItem *history.MessageIdItem) (int, error) {
./cmd/web/main.go:95:func (pa *ProcessorAdapter) CheckNoMoreWorkInHistory() bool {
./internal/cache/newsgroup_cache.go:112:func (nc *NewsgroupCache) Set(page, pageSize int, groups []*Newsgroup, totalCount int) {
./internal/cache/newsgroup_cache.go:144:func (nc *NewsgroupCache) Remove(page, pageSize int) {
./internal/cache/newsgroup_cache.go:158:func (nc *NewsgroupCache) Clear() {
./internal/cache/newsgroup_cache.go:170:func (nc *NewsgroupCache) GetStats() map[string]interface{} {
./internal/cache/newsgroup_cache.go:206:func (nc *NewsgroupCache) GetCachedSize() int64 {
./internal/cache/newsgroup_cache.go:213:func (nc *NewsgroupCache) GetCachedSizeHuman() string {
./internal/cache/newsgroup_cache.go:225:func (nc *NewsgroupCache) getCachedSize() int64 {
./internal/cache/newsgroup_cache.go:232:func (nc *NewsgroupCache) updateCachedSize(delta int64) {
./internal/cache/newsgroup_cache.go:242:func (nc *NewsgroupCache) estimateSize(groups []*Newsgroup) int64 {
./internal/cache/newsgroup_cache.go:262:func (nc *NewsgroupCache) evictIfNeeded() {
./internal/cache/newsgroup_cache.go:288:func (nc *NewsgroupCache) cleanup() {
./internal/cache/newsgroup_cache.go:304:func (nc *NewsgroupCache) cleanupExpired() {
./internal/cache/newsgroup_cache.go:330:func (nc *NewsgroupCache) Stop() {
./internal/cache/newsgroup_cache.go:67:func (nc *NewsgroupCache) generateKey(page, pageSize int) string {
./internal/cache/newsgroup_cache.go:72:func (nc *NewsgroupCache) Get(page, pageSize int) ([]*Newsgroup, int, bool) {
./internal/cache/sanitized_cache.go:117:func (sc *SanitizedCache) GetArticle(messageID string) (*SanitizedArticle, bool) {
./internal/cache/sanitized_cache.go:137:func (sc *SanitizedCache) SetField(messageID string, field string, value template.HTML) {
./internal/cache/sanitized_cache.go:190:func (sc *SanitizedCache) SetArticle(messageID string, sanitizedFields map[string]template.HTML) {
./internal/cache/sanitized_cache.go:239:func (sc *SanitizedCache) BatchSetArticles(articles map[string]map[string]template.HTML) {
./internal/cache/sanitized_cache.go:298:func (sc *SanitizedCache) Clear() {
./internal/cache/sanitized_cache.go:305:func (sc *SanitizedCache) Stats() map[string]interface{} {
./internal/cache/sanitized_cache.go:332:func (sc *SanitizedCache) Stop() {
./internal/cache/sanitized_cache.go:337:func (sc *SanitizedCache) hashMessageID(messageID string) string {
./internal/cache/sanitized_cache.go:350:func (sc *SanitizedCache) evictOldest() {
./internal/cache/sanitized_cache.go:376:func (sc *SanitizedCache) cleanupLoop() {
./internal/cache/sanitized_cache.go:391:func (sc *SanitizedCache) cleanup() {
./internal/cache/sanitized_cache.go:39:func (sc *SanitizedCache) GetCachedSize() int64 {
./internal/cache/sanitized_cache.go:46:func (sc *SanitizedCache) GetCachedSizeHuman() string {
./internal/cache/sanitized_cache.go:74:func (sc *SanitizedCache) GetField(messageID string, field string) (template.HTML, bool) {
./internal/database/activeDB.go:121:func (a *ActiveDB) GetNewsgroupByID(groupID int) (*models.ActiveNewsgroup, error) {
./internal/database/activeDB.go:143:func (a *ActiveDB) ListNewsgroups() ([]*models.ActiveNewsgroup, error) {
./internal/database/activeDB.go:172:func (a *ActiveDB) UpdateWatermarks(groupID int, highWater, lowWater int) error {
./internal/database/activeDB.go:184:func (a *ActiveDB) UpdateMessageCount(groupID int, messageCount int) error {
./internal/database/activeDB.go:196:func (a *ActiveDB) SetStatus(groupID int, status string) error {
./internal/database/activeDB.go:208:func (a *ActiveDB) RemoveNewsgroup(groupID int) error {
./internal/database/activeDB.go:215:func (a *ActiveDB) GetDB() *sql.DB {
./internal/database/activeDB.go:48:func (a *ActiveDB) initSchema() error {
./internal/database/activeDB.go:72:func (a *ActiveDB) Close() error {
./internal/database/activeDB.go:77:func (a *ActiveDB) AddNewsgroup(groupName, description string) (*models.ActiveNewsgroup, error) {
./internal/database/activeDB.go:99:func (a *ActiveDB) GetNewsgroup(groupName string) (*models.ActiveNewsgroup, error) {
./internal/database/article_cache.go:121:func (ac *ArticleCache) evictIfNeeded() {
./internal/database/article_cache.go:132:func (ac *ArticleCache) removeElement(elem *list.Element) {
./internal/database/article_cache.go:147:func (ac *ArticleCache) Remove(groupName string, articleNum int64) {
./internal/database/article_cache.go:158:func (ac *ArticleCache) Clear() {
./internal/database/article_cache.go:168:func (ac *ArticleCache) ClearGroup(groupName string) {
./internal/database/article_cache.go:189:func (ac *ArticleCache) Stats() map[string]interface{} {
./internal/database/article_cache.go:220:func (ac *ArticleCache) Cleanup() {
./internal/database/article_cache.go:45:func (ac *ArticleCache) makeKey(groupName string, articleNum int64) string {
./internal/database/article_cache.go:50:func (ac *ArticleCache) Get(groupName string, articleNum int64) (*models.Article, bool) {
./internal/database/article_cache.go:84:func (ac *ArticleCache) Put(groupName string, articleNum int64, article *models.Article) {
./internal/database/database.go:185:func (db *Database) Shutdown() error {
./internal/database/database.go:18:func (db *Database) GetMainDB() *sql.DB {
./internal/database/database.go:22:func (db *Database) CronDB() {
./internal/database/database.go:250:func (db *Database) GetDataDir() string {
./internal/database/database.go:271:func (db *Database) GetStats() *Stats {
./internal/database/database.go:315:func (db *Database) GetHistoryUseShortHashLen(defaultValue int) (int, bool, error) {
./internal/database/database.go:347:func (db *Database) SetHistoryUseShortHashLen(value int) error {
./internal/database/database.go:390:func (db *Database) SetShutdownState(state string) error {
./internal/database/database.go:420:func (db *Database) GetShutdownState() (string, error) {
./internal/database/database.go:435:func (db *Database) InitializeSystemStatus(appVersion string, pid int, hostname string) error {
./internal/database/database.go:44:func (db *Database) cleanupIdleGroups() {
./internal/database/database.go:462:func (db *Database) CheckPreviousShutdown() (bool, error) {
./internal/database/database.go:479:func (db *Database) IsShuttingDown() bool {
./internal/database/database.go:489:func (db *Database) UpdateHeartbeat() {
./internal/database/database.go:507:func (db *Database) GetNewsgroupID(groupName string) (int, error) {
./internal/database/database.go:517:func (db *Database) IncrementArticleSpam(groupName string, articleNum int64) error {
./internal/database/database.go:559:func (db *Database) IncrementArticleHide(groupName string, articleNum int64) error {
./internal/database/database.go:575:func (db *Database) UnHideArticle(groupName string, articleNum int64) error {
./internal/database/database.go:591:func (db *Database) DecrementArticleSpam(groupName string, articleNum int64) error {
./internal/database/database.go:648:func (db *Database) HasUserFlaggedSpam(userID int, groupName string, articleNum int64) (bool, error) {
./internal/database/database.go:669:func (db *Database) RecordUserSpamFlag(userID int, groupName string, articleNum int64) error {
./internal/database/database.go:81:func (db *Database) removePartialInitializedGroupDB(groupName string) {
./internal/database/database.go:88:func (db *Database) GetGroupDBs(groupName string) (*GroupDBs, error) {
./internal/database/db_active.go:10:func (db *Database) initActiveDB() error {
./internal/database/db_active.go:21:func (db *Database) GetActiveDB() *ActiveDB {
./internal/database/db_active.go:28:func (db *Database) AddActiveNewsgroup(groupName, description string) (*models.ActiveNewsgroup, error) {
./internal/database/db_active.go:33:func (db *Database) GetActiveNewsgroup(groupName string) (*models.ActiveNewsgroup, error) {
./internal/database/db_active.go:38:func (db *Database) GetActiveNewsgroupByID(groupID int) (*models.ActiveNewsgroup, error) {
./internal/database/db_active.go:43:func (db *Database) ListActiveNewsgroups() ([]*models.ActiveNewsgroup, error) {
./internal/database/db_active.go:48:func (db *Database) UpdateActiveWatermarks(groupID int, highWater, lowWater int) error {
./internal/database/db_active.go:53:func (db *Database) UpdateActiveMessageCount(groupID int, messageCount int) error {
./internal/database/db_active.go:58:func (db *Database) SetActiveNewsgroupStatus(groupID int, status string) error {
./internal/database/db_active.go:63:func (db *Database) RemoveActiveNewsgroup(groupID int) error {
./internal/database/db_aimodels.go:118:func (db *Database) CreateAIModel(postKey, ollamaModelName, displayName, description string, isActive, isDefault bool, sortOrder int) (*models.AIModel, error) {
./internal/database/db_aimodels.go:12:func (db *Database) GetActiveAIModels() ([]*models.AIModel, error) {
./internal/database/db_aimodels.go:152:func (db *Database) UpdateAIModel(id int, ollamaModelName, displayName, description string, isActive, isDefault bool, sortOrder int) error {
./internal/database/db_aimodels.go:167:func (db *Database) SetDefaultAIModel(id int) error {
./internal/database/db_aimodels.go:185:func (db *Database) DeleteAIModel(id int) error {
./internal/database/db_aimodels.go:194:func (db *Database) GetAllAIModels() ([]*models.AIModel, error) {
./internal/database/db_aimodels.go:45:func (db *Database) GetDefaultAIModel() (*models.AIModel, error) {
./internal/database/db_aimodels.go:72:func (db *Database) GetFirstActiveAIModel() (*models.AIModel, error) {
./internal/database/db_aimodels.go:96:func (db *Database) GetAIModelByPostKey(postKey string) (*models.AIModel, error) {
./internal/database/db_apitokens.go:109:func (db *Database) UpdateTokenUsage(tokenID int) error {
./internal/database/db_apitokens.go:122:func (db *Database) ListAPITokens() ([]*APIToken, error) {
./internal/database/db_apitokens.go:154:func (db *Database) DisableAPIToken(tokenID int) error {
./internal/database/db_apitokens.go:164:func (db *Database) EnableAPIToken(tokenID int) error {
./internal/database/db_apitokens.go:174:func (db *Database) DeleteAPIToken(tokenID int) error {
./internal/database/db_apitokens.go:184:func (db *Database) CleanupExpiredTokens() (int, error) {
./internal/database/db_apitokens.go:39:func (db *Database) CreateAPIToken(ownerName string, ownerID int, expiresAt *time.Time) (*APIToken, string, error) {
./internal/database/db_apitokens.go:80:func (db *Database) ValidateAPIToken(plainToken string) (*APIToken, error) {
./internal/database/db_batch.go:1026:func (o *BatchOrchestrator) StartOrch() {
./internal/database/db_batch.go:1064:func (o *BatchOrchestrator) StartOrchestrator() {
./internal/database/db_batch.go:1118:func (o *BatchOrchestrator) checkThresholds() (haswork bool) {
./internal/database/db_batch.go:117:func (sq *SQ3batch) BatchCaptureOverviewForLater(newsgroupPtr *string, article *models.Article) {
./internal/database/db_batch.go:1200:func (sq *SQ3batch) BatchDivider() {
./internal/database/db_batch.go:125:func (sq *SQ3batch) GetNewsgroupPointer(newsgroup string) *string {
./internal/database/db_batch.go:149:func (sq *SQ3batch) GetChan(newsgroup *string) chan *OverviewBatch {
./internal/database/db_batch.go:159:func (sq *SQ3batch) GetOrCreateTasksMapKey(newsgroup string) *BatchTasks {
./internal/database/db_batch.go:183:func (c *SQ3batch) CheckNoMoreWorkInMaps() bool {
./internal/database/db_batch.go:255:func (c *SQ3batch) processAllPendingBatches(wgProcessAllBatches *sync.WaitGroup) {
./internal/database/db_batch.go:348:func (c *SQ3batch) processNewsgroupBatch(task *BatchTasks) {
./internal/database/db_batch.go:484:func (c *SQ3batch) batchInsertOverviews(newsgroup string, batches []*OverviewBatch) ([]int64, error) {
./internal/database/db_batch.go:490:func (c *SQ3batch) batchInsertOverviewsWithDBs(newsgroup string, batches []*OverviewBatch) ([]int64, *GroupDBs, error) {
./internal/database/db_batch.go:541:func (c *SQ3batch) processSingleOverviewBatch(groupDBs *GroupDBs, batches []*OverviewBatch) ([]int64, error) {
./internal/database/db_batch.go:638:func (c *SQ3batch) batchProcessThreading(groupName *string, batches []*OverviewBatch, articleNumbers []int64) error {
./internal/database/db_batch.go:649:func (c *SQ3batch) batchProcessThreadingWithDBs(groupName *string, batches []*OverviewBatch, articleNumbers []int64, groupDBs *GroupDBs) error {
./internal/database/db_batch.go:705:func (c *SQ3batch) batchProcessThreadRoots(groupDBs *GroupDBs, batches []*OverviewBatch, articleNumbers []int64, rootIndices []int) error {
./internal/database/db_batch.go:73:func (c *SQ3batch) SetProcessor(proc ThreadingProcessor) {
./internal/database/db_batch.go:751:func (c *SQ3batch) batchProcessReplies(groupDBs *GroupDBs, batches []*OverviewBatch, articleNumbers []int64, replyIndices []int) error {
./internal/database/db_batch.go:838:func (c *SQ3batch) batchUpdateReplyCounts(groupDBs *GroupDBs, parentCounts map[string]int, tableName string) error {
./internal/database/db_batch.go:877:func (c *SQ3batch) findThreadRootForBatch(groupDBs *GroupDBs, refs []string) (int64, error) {
./internal/database/db_batch.go:905:func (c *SQ3batch) batchUpdateThreadCache(groupDBs *GroupDBs, threadUpdates map[int64][]threadCacheUpdateData) error {
./internal/database/db_config.go:21:func (db *Database) SetConfigValue(key, value string) error {
./internal/database/db_config.go:30:func (db *Database) GetConfigBool(key string) (bool, error) {
./internal/database/db_config.go:39:func (db *Database) SetConfigBool(key string, value bool) error {
./internal/database/db_config.go:50:func (db *Database) IsRegistrationEnabled() (bool, error) {
./internal/database/db_config.go:8:func (db *Database) GetConfigValue(key string) (string, error) {
./internal/database/db_groupdbs.go:26:func (dbs *GroupDBs) IncrementWorkers() {
./internal/database/db_groupdbs.go:34:func (dbs *GroupDBs) Return(db *Database) {
./internal/database/db_groupdbs.go:78:func (db *GroupDBs) ExistsMsgIdInArticlesDB(messageID string) bool {
./internal/database/db_groupdbs.go:90:func (dbs *GroupDBs) Close() error {
./internal/database/db_init.go:183:func (db *Database) IsDBshutdown() bool {
./internal/database/db_init.go:199:func (db *Database) initMainDB() error {
./internal/database/db_init.go:240:func (db *Database) applySQLitePragmas(conn *sql.DB) error {
./internal/database/db_init.go:265:func (db *Database) applySQLitePragmasGroupDB(conn *sql.DB) error {
./internal/database/db_init.go:290:func (db *Database) LoadDefaultProviders() error {
./internal/database/db_migrate.go:228:func (db *Database) migrateActiveDB() error {
./internal/database/db_migrate.go:273:func (db *Database) migrateMainDB() error {
./internal/database/db_migrate.go:309:func (db *Database) MigrateGroup(groupName string) error {
./internal/database/db_migrate.go:321:func (db *Database) migrateGroupDB(groupDBs *GroupDBs) error {
./internal/database/db_migrate.go:54:func (db *Database) Migrate() error {
./internal/database/db_nntp_users.go:111:func (db *Database) UpdateNNTPUserLastLogin(userID int) error {
./internal/database/db_nntp_users.go:118:func (db *Database) UpdateNNTPUserPermissions(userID int, maxConns int, posting bool) error {
./internal/database/db_nntp_users.go:125:func (db *Database) DeactivateNNTPUser(userID int) error {
./internal/database/db_nntp_users.go:132:func (db *Database) ActivateNNTPUser(userID int) error {
./internal/database/db_nntp_users.go:139:func (db *Database) DeleteNNTPUser(userID int) error {
./internal/database/db_nntp_users.go:154:func (db *Database) CreateNNTPSession(userID int, connectionID, remoteAddr string) error {
./internal/database/db_nntp_users.go:15:func (db *Database) InsertNNTPUser(u *models.NNTPUser) error {
./internal/database/db_nntp_users.go:161:func (db *Database) UpdateNNTPSessionActivity(connectionID string) error {
./internal/database/db_nntp_users.go:168:func (db *Database) CloseNNTPSession(connectionID string) error {
./internal/database/db_nntp_users.go:175:func (db *Database) GetActiveNNTPSessionsForUser(userID int) (int, error) {
./internal/database/db_nntp_users.go:183:func (db *Database) CleanupOldNNTPSessions(olderThan time.Duration) error {
./internal/database/db_nntp_users.go:211:func (db *Database) CreateNNTPUserForWebUser(webUserID int64) error {
./internal/database/db_nntp_users.go:242:func (db *Database) AuthenticateNNTPUser(username, password string) (*models.NNTPUser, error) {
./internal/database/db_nntp_users.go:276:func (db *Database) InvalidateNNTPUserAuth(username string) {
./internal/database/db_nntp_users.go:283:func (db *Database) GetNNTPAuthCacheStats() map[string]interface{} {
./internal/database/db_nntp_users.go:29:func (db *Database) GetNNTPUserByUsername(username string) (*models.NNTPUser, error) {
./internal/database/db_nntp_users.go:44:func (db *Database) GetNNTPUserByID(id int) (*models.NNTPUser, error) {
./internal/database/db_nntp_users.go:59:func (db *Database) GetAllNNTPUsers() ([]*models.NNTPUser, error) {
./internal/database/db_nntp_users.go:82:func (db *Database) VerifyNNTPUserPassword(username, password string) (*models.NNTPUser, error) {
./internal/database/db_nntp_users.go:98:func (db *Database) UpdateNNTPUserPassword(userID int, password string) error {
./internal/database/db_rescan.go:103:func (db *Database) CheckDatabaseConsistency(newsgroup string) (*ConsistencyReport, error) {
./internal/database/db_rescan.go:10:func (db *Database) Rescan(newsgroup string) error {
./internal/database/db_rescan.go:185:func (db *Database) findMissingArticles(groupDB *GroupDBs, maxArticleNum int64) []int64 {
./internal/database/db_rescan.go:221:func (db *Database) findOrphanedThreads(groupDB *GroupDBs) []int64 {
./internal/database/db_rescan.go:262:func (report *ConsistencyReport) PrintReport() {
./internal/database/db_rescan.go:38:func (db *Database) GetLatestArticleNumberFromOverview(newsgroup string) (int64, error) {
./internal/database/db_rescan.go:58:func (db *Database) GetLatestArticleNumbers(newsgroup string) (map[string]int64, error) {
./internal/database/db_sections.go:11:func (db *Database) GetAllSections() ([]*models.Section, error) {
./internal/database/db_sections.go:125:func (db *Database) GetSectionByID(id int) (*models.Section, error) {
./internal/database/db_sections.go:155:func (db *Database) SectionNameExists(name string) (bool, error) {
./internal/database/db_sections.go:168:func (db *Database) SectionNameExistsExcluding(name string, excludeID int) (bool, error) {
./internal/database/db_sections.go:181:func (db *Database) CreateSection(section *models.Section) error {
./internal/database/db_sections.go:211:func (db *Database) UpdateSection(section *models.Section) error {
./internal/database/db_sections.go:245:func (db *Database) DeleteSection(id int) error {
./internal/database/db_sections.go:279:func (db *Database) GetSectionGroupByID(id int) (*models.SectionGroup, error) {
./internal/database/db_sections.go:308:func (db *Database) SectionGroupExists(sectionID int, newsgroupName string) (bool, error) {
./internal/database/db_sections.go:321:func (db *Database) CreateSectionGroup(sg *models.SectionGroup) error {
./internal/database/db_sections.go:350:func (db *Database) DeleteSectionGroup(id int) error {
./internal/database/db_sections.go:371:func (db *Database) GetNewsgroupByName(name string) (*models.Newsgroup, error) {
./internal/database/db_sections.go:47:func (db *Database) GetAllSectionsWithCounts() ([]*models.Section, error) {
./internal/database/db_sections.go:90:func (db *Database) GetAllSectionGroups() ([]*models.SectionGroup, error) {
./internal/database/db_sessions.go:104:func (db *Database) InvalidateUserSessionBySessionID(sessionID string) error {
./internal/database/db_sessions.go:115:func (db *Database) IncrementLoginAttempts(username string) error {
./internal/database/db_sessions.go:126:func (db *Database) ResetLoginAttempts(userID int) error {
./internal/database/db_sessions.go:137:func (db *Database) IsUserLockedOut(username string) (bool, error) {
./internal/database/db_sessions.go:164:func (db *Database) CleanupExpiredSessions() error {
./internal/database/db_sessions.go:30:func (db *Database) CreateUserSession(userID int, remoteIP string) (string, error) {
./internal/database/db_sessions.go:58:func (db *Database) ValidateUserSession(sessionID string) (*models.User, error) {
./internal/database/db_sessions.go:93:func (db *Database) InvalidateUserSession(userID int) error {
./internal/database/groups_hashmap.go:112:func (h *GHmap) GroupToHash(group string) string {
./internal/database/groups_hashmap.go:141:func (h *GHmap) GetGroupFromHash(hash string) (string, bool) {
./internal/database/groups_hashmap.go:54:func (h *GHmap) Ghit() {
./internal/database/groups_hashmap.go:61:func (h *GHmap) Hhit() {
./internal/database/groups_hashmap.go:68:func (h *GHmap) Gneg() {
./internal/database/groups_hashmap.go:75:func (h *GHmap) Hneg() {
./internal/database/groups_hashmap.go:82:func (h *GHmap) GHmapCron() {
./internal/database/nntp_auth_cache.go:101:func (c *NNTPAuthCache) Clear() {
./internal/database/nntp_auth_cache.go:113:func (c *NNTPAuthCache) Stats() map[string]interface{} {
./internal/database/nntp_auth_cache.go:154:func (c *NNTPAuthCache) cleanupExpired() {
./internal/database/nntp_auth_cache.go:44:func (c *NNTPAuthCache) generatePasswordHash(password string) string {
./internal/database/nntp_auth_cache.go:50:func (c *NNTPAuthCache) Set(userID int, username, password string) {
./internal/database/nntp_auth_cache.go:65:func (c *NNTPAuthCache) Get(username, password string) (int, bool) {
./internal/database/nntp_auth_cache.go:93:func (c *NNTPAuthCache) Remove(username string) {
./internal/database/progress.go:100:func (p *ProgressDB) UpdateProgress(backendName, newsgroupName string, lastArticle int64) error {
./internal/database/progress.go:119:func (p *ProgressDB) GetAllProgress() ([]*ProgressEntry, error) {
./internal/database/progress.go:156:func (p *ProgressDB) GetProgressForBackend(backendName string) ([]*ProgressEntry, error) {
./internal/database/progress.go:194:func (p *ProgressDB) Close() error {
./internal/database/progress.go:57:func (p *ProgressDB) initSchema() error {
./internal/database/progress.go:82:func (p *ProgressDB) GetLastArticle(backendName, newsgroupName string) (int64, error) {
./internal/database/queries.go:1011:func (db *Database) GetProviderByID(id int) (*models.Provider, error) {
./internal/database/queries.go:1028:func (db *Database) IsNewsGroupInSections(name string) bool {
./internal/database/queries.go:1048:func (db *Database) GetTopGroupsByMessageCount(limit int) ([]*models.Newsgroup, error) {
./internal/database/queries.go:107:func (db *Database) InsertNewsgroup(g *models.Newsgroup) error {
./internal/database/queries.go:1082:func (db *Database) GetTotalThreadsCount() (int64, error) {
./internal/database/queries.go:1112:func (db *Database) SearchNewsgroups(searchTerm string) ([]*models.Newsgroup, error) {
./internal/database/queries.go:1147:func (db *Database) GetAllUsers() ([]*models.User, error) {
./internal/database/queries.go:1166:func (db *Database) GetOverviewsRange(groupDBs *GroupDBs, startNum, endNum int64) ([]*models.Overview, error) {
./internal/database/queries.go:1201:func (db *Database) GetOverviewByMessageID(groupDBs *GroupDBs, messageID string) (*models.Overview, error) {
./internal/database/queries.go:120:func (db *Database) MainDBGetNewsgroupsCount() int64 {
./internal/database/queries.go:1226:func (db *Database) GetHeaderFieldRange(groupDBs *GroupDBs, field string, startNum, endNum int64) (map[int64]string, error) {
./internal/database/queries.go:1295:func (db *Database) UpdateNewsgroupWatermarks(name string, highWater, lowWater int) error {
./internal/database/queries.go:1304:func (db *Database) UpdateNewsgroupStatus(name string, status string) error {
./internal/database/queries.go:1313:func (db *Database) GetNewsgroupsByPattern(pattern string) ([]*models.Newsgroup, error) {
./internal/database/queries.go:132:func (db *Database) MainDBGetAllNewsgroups() ([]*models.Newsgroup, error) {
./internal/database/queries.go:1343:func (db *Database) GetAllHierarchies() ([]*models.Hierarchy, error) {
./internal/database/queries.go:1371:func (db *Database) GetHierarchiesPaginated(page, pageSize int, sortBy string) ([]*models.Hierarchy, int, error) {
./internal/database/queries.go:1433:func (db *Database) UpdateHierarchiesLastUpdated() error {
./internal/database/queries.go:1470:func (db *Database) UpdateHierarchyCounts() error {
./internal/database/queries.go:1516:func (db *Database) GetNewsgroupsByHierarchy(hierarchy string, page, pageSize int, sortBy string) ([]*models.Newsgroup, int, error) {
./internal/database/queries.go:151:func (db *Database) MainDBGetNewsgroup(newsgroup string) (*models.Newsgroup, error) {
./internal/database/queries.go:1568:func (db *Database) DeleteUser(userID int64) error {
./internal/database/queries.go:1626:func (db *Database) ResetAllNewsgroupData() error {
./internal/database/queries.go:1674:func (db *Database) ResetNewsgroupData(newsgroupName string) error {
./internal/database/queries.go:173:func (db *Database) UpdateNewsgroup(g *models.Newsgroup) error {
./internal/database/queries.go:1754:func (db *Database) ResetNewsgroupCounters(newsgroupName string) error {
./internal/database/queries.go:1779:func (db *Database) GetAllSiteNews() ([]*models.SiteNews, error) {
./internal/database/queries.go:1808:func (db *Database) GetVisibleSiteNews() ([]*models.SiteNews, error) {
./internal/database/queries.go:1837:func (db *Database) GetSiteNewsByID(id int) (*models.SiteNews, error) {
./internal/database/queries.go:1860:func (db *Database) CreateSiteNews(news *models.SiteNews) error {
./internal/database/queries.go:187:func (db *Database) UpdateNewsgroupExpiry(name string, expiryDays int) error {
./internal/database/queries.go:1885:func (db *Database) UpdateSiteNews(news *models.SiteNews) error {
./internal/database/queries.go:1904:func (db *Database) DeleteSiteNews(id int) error {
./internal/database/queries.go:1913:func (db *Database) ToggleSiteNewsVisibility(id int) error {
./internal/database/queries.go:1926:func (db *Database) GetSpamArticles(offset, limit int) ([]*models.Overview, []string, int, error) {
./internal/database/queries.go:196:func (db *Database) UpdateNewsgroupExpiryPrefix(name string, expiryDays int) error {
./internal/database/queries.go:205:func (db *Database) UpdateNewsgroupMaxArticles(name string, maxArticles int) error {
./internal/database/queries.go:214:func (db *Database) UpdateNewsgroupMaxArticlesPrefix(name string, maxArticles int) error {
./internal/database/queries.go:223:func (db *Database) UpdateNewsgroupActive(name string, active bool) error {
./internal/database/queries.go:232:func (db *Database) BulkUpdateNewsgroupActive(names []string, active bool) (int, error) {
./internal/database/queries.go:277:func (db *Database) BulkDeleteNewsgroups(names []string) (int, error) {
./internal/database/queries.go:321:func (db *Database) UpdateNewsgroupDescription(name string, description string) error {
./internal/database/queries.go:330:func (db *Database) DeleteNewsgroup(name string) error {
./internal/database/queries.go:336:func (db *Database) GetThreadsCount(groupDBs *GroupDBs) (int64, error) {
./internal/database/queries.go:346:func (db *Database) GetArticlesCount(groupDBs *GroupDBs) (int64, error) {
./internal/database/queries.go:358:func (db *Database) GetLastArticleDate(groupDBs *GroupDBs) (*time.Time, error) {
./internal/database/queries.go:381:func (db *Database) GetArticles(groupDBs *GroupDBs) ([]*models.Article, error) {
./internal/database/queries.go:401:func (db *Database) InsertThread(groupDBs *GroupDBs, t *models.Thread, a *models.Article) error {
./internal/database/queries.go:410:func (db *Database) GetThreads(groupDBs *GroupDBs) ([]*models.Thread, error) {
./internal/database/queries.go:432:func (db *Database) InsertOverview(groupDBs *GroupDBs, o *models.Overview) (int64, error) {
./internal/database/queries.go:455:func (db *Database) GetOverviews(groupDBs *GroupDBs) ([]*models.Overview, error) {
./internal/database/queries.go:476:func (db *Database) SetOverviewDownloaded(groupDBs *GroupDBs, articleNum int64, downloaded int) error {
./internal/database/queries.go:48:func (db *Database) AddProvider(provider *models.Provider) error {
./internal/database/queries.go:492:func (db *Database) GetUndownloadedOverviews(groupDBs *GroupDBs, fetchMax int) ([]*models.Overview, error) {
./internal/database/queries.go:510:func (db *Database) InsertUser(u *models.User) error {
./internal/database/queries.go:518:func (db *Database) GetUserByUsername(username string) (*models.User, error) {
./internal/database/queries.go:527:func (db *Database) GetUserByEmail(email string) (*models.User, error) {
./internal/database/queries.go:536:func (db *Database) GetUserByID(id int64) (*models.User, error) {
./internal/database/queries.go:546:func (db *Database) UpdateUserEmail(userID int64, email string) error {
./internal/database/queries.go:552:func (db *Database) UpdateUserPassword(userID int64, passwordHash string) error {
./internal/database/queries.go:558:func (db *Database) InsertSession(s *models.Session) error {
./internal/database/queries.go:566:func (db *Database) GetSession(id string) (*models.Session, error) {
./internal/database/queries.go:575:func (db *Database) DeleteSession(id string) error {
./internal/database/queries.go:581:func (db *Database) InsertUserPermission(up *models.UserPermission) error {
./internal/database/queries.go:589:func (db *Database) GetUserPermissions(userID int) ([]*models.UserPermission, error) {
./internal/database/queries.go:607:func (db *Database) GetArticleByNum(groupDBs *GroupDBs, articleNum int64) (*models.Article, error) {
./internal/database/queries.go:630:func (db *Database) GetArticleByMessageID(groupDBs *GroupDBs, messageID string) (*models.Article, error) {
./internal/database/queries.go:63:func (db *Database) DeleteProvider(id int) error {
./internal/database/queries.go:648:func (db *Database) UpdateReplyCount(groupDBs *GroupDBs, messageID string, replyCount int) error {
./internal/database/queries.go:657:func (db *Database) IncrementReplyCount(groupDBs *GroupDBs, messageID string) error {
./internal/database/queries.go:666:func (db *Database) GetReplyCount(groupDBs *GroupDBs, messageID string) (int, error) {
./internal/database/queries.go:679:func (db *Database) UpdateOverviewReplyCount(groupDBs *GroupDBs, messageID string, replyCount int) error {
./internal/database/queries.go:688:func (db *Database) IncrementOverviewReplyCount(groupDBs *GroupDBs, messageID string) error {
./internal/database/queries.go:698:func (db *Database) GetActiveNewsgroupByName(name string) (*models.Newsgroup, error) {
./internal/database/queries.go:708:func (db *Database) UpsertNewsgroupDescription(name, description string) error {
./internal/database/queries.go:719:func (db *Database) GetActiveNewsgroups() ([]*models.Newsgroup, error) {
./internal/database/queries.go:71:func (db *Database) SetProvider(provider *models.Provider) error {
./internal/database/queries.go:737:func (db *Database) GetActiveNewsgroupsWithMessages() ([]*models.Newsgroup, error) {
./internal/database/queries.go:755:func (db *Database) GetNewsgroupsPaginated(page, pageSize int) ([]*models.Newsgroup, int, error) {
./internal/database/queries.go:789:func (db *Database) GetNewsgroupsPaginatedAdmin(page, pageSize int) ([]*models.Newsgroup, int, error) {
./internal/database/queries.go:824:func (db *Database) GetOverviewsPaginated(groupDBs *GroupDBs, lastArticleNum int64, pageSize int) ([]*models.Overview, int, bool, error) {
./internal/database/queries.go:893:func (db *Database) InsertSection(s *models.Section) error {
./internal/database/queries.go:89:func (db *Database) GetProviders() ([]*models.Provider, error) {
./internal/database/queries.go:902:func (db *Database) GetSections() ([]*models.Section, error) {
./internal/database/queries.go:920:func (db *Database) GetSectionByName(name string) (*models.Section, error) {
./internal/database/queries.go:930:func (db *Database) GetHeaderSections() ([]*models.Section, error) {
./internal/database/queries.go:950:func (db *Database) InsertSectionGroup(sg *models.SectionGroup) error {
./internal/database/queries.go:959:func (db *Database) GetSectionGroups(sectionID int) ([]*models.SectionGroup, error) {
./internal/database/queries.go:977:func (db *Database) GetSectionGroupsByName(newsgroupName string) ([]*models.SectionGroup, error) {
./internal/database/queries.go:994:func (db *Database) GetProviderByName(name string) (*models.Provider, error) {
./internal/database/sections_cache.go:26:func (g *GroupSectionDBCache) IsInSections(group string) bool {
./internal/database/sections_cache.go:41:func (g *GroupSectionDBCache) CronClean() {
./internal/database/sections_cache.go:73:func (g *GroupSectionDBCache) AddGroupToSectionsCache(group string) {
./internal/database/thread_cache.go:155:func (mem *MemCachedThreads) CleanCron() {
./internal/database/thread_cache.go:182:func (mem *MemCachedThreads) GetMemCachedTreadsCount(group string) int64 {
./internal/database/thread_cache.go:201:func (db *Database) GetCachedThreads(groupDBs *GroupDBs, page int64, pageSize int64) ([]*models.ForumThread, int64, error) {
./internal/database/thread_cache.go:28:func (db *Database) InitializeThreadCache(groupDBs *GroupDBs, threadRoot int64, rootArticle *models.Article) error {
./internal/database/thread_cache.go:300:func (db *Database) GetCachedThreadReplies(groupDBs *GroupDBs, threadRoot int64, page int, pageSize int) ([]*models.Overview, int, error) {
./internal/database/thread_cache.go:378:func (db *Database) GetOverviewByArticleNum(groupDBs *GroupDBs, articleNum int64) (*models.Overview, error) {
./internal/database/thread_cache.go:402:func (mem *MemCachedThreads) GetCachedThreadsFromMemory(db *Database, groupDBs *GroupDBs, group string, page int64, pageSize int64) ([]*models.ForumThread, int64, bool) {
./internal/database/thread_cache.go:484:func (mem *MemCachedThreads) RefreshThreadCache(db *Database, groupDBs *GroupDBs, group string, requestedPage int64, pageSize int64) error {
./internal/database/thread_cache.go:55:func (db *Database) UpdateThreadCache(groupDBs *GroupDBs, threadRoot int64, childArticleNum int64, childDate time.Time) error {
./internal/database/thread_cache.go:597:func (mem *MemCachedThreads) InvalidateThreadRoot(group string, threadRoot int64) {
./internal/database/thread_cache.go:627:func (mem *MemCachedThreads) UpdateThreadMetadata(group string, threadRoot int64, messageCount int, lastActivity time.Time, childArticles string) {
./internal/database/thread_cache.go:685:func (mem *MemCachedThreads) InvalidateGroup(group string) {
./internal/database/tree_cache.go:199:func (db *Database) GetCachedTree(groupDBs *GroupDBs, threadRoot int64) (*ThreadTree, error) {
./internal/database/tree_cache.go:288:func (db *Database) CacheTreeStructure(groupDBs *GroupDBs, tree *ThreadTree) error {
./internal/database/tree_cache.go:388:func (db *Database) InvalidateTreeCache(groupDBs *GroupDBs, threadRoot int64) error {
./internal/database/tree_cache.go:405:func (tree *ThreadTree) calculateTreeStats() {
./internal/database/tree_cache.go:427:func (tree *ThreadTree) countDescendants(node *TreeNode) int {
./internal/database/tree_cache.go:435:func (tree *ThreadTree) assignSortOrder() {
./internal/database/tree_cache.go:440:func (tree *ThreadTree) assignSortOrderRecursive(node *TreeNode, sortOrder *int, pathPrefix string) {
./internal/database/tree_cache.go:463:func (tree *ThreadTree) GetTreeStructureJSON() (string, error) {
./internal/database/tree_cache.go:53:func (tree *ThreadTree) GetTreeStats() TreeStats {
./internal/database/tree_cache.go:66:func (db *Database) BuildThreadTree(groupDBs *GroupDBs, threadRoot int64) (*ThreadTree, error) {
./internal/database/tree_view_api.go:101:func (db *Database) HandleThreadTreeAPI(w http.ResponseWriter, r *http.Request) {
./internal/database/tree_view_api.go:169:func (tree *ThreadTree) PrintTreeASCII() {
./internal/database/tree_view_api.go:179:func (tree *ThreadTree) printNodeASCII(node *TreeNode, prefix string, isLast bool) {
./internal/database/tree_view_api.go:219:func (tree *ThreadTree) GetThreadTreeHTML(groupName string) template.HTML {
./internal/database/tree_view_api.go:242:func (tree *ThreadTree) getNodeHTML(node *TreeNode, groupName string) string {
./internal/database/tree_view_api.go:34:func (db *Database) GetThreadTreeView(groupDBs *GroupDBs, threadRoot int64, options TreeViewOptions) (*TreeViewResponse, error) {
./internal/database/tree_view_api.go:70:func (db *Database) loadOverviewDataForTree(groupDBs *GroupDBs, tree *ThreadTree) error {
./internal/database/tree_view_api.go:85:func (tree *ThreadTree) limitDepth(maxDepth int) {
./internal/database/tree_view_api.go:89:func (tree *ThreadTree) limitDepthRecursive(node *TreeNode, maxDepth int) {
./internal/fediverse/activitypub.go:100:func (aps *ActivityPubServer) ArticleToNote(article *models.Article, newsgroup string) *Note {
./internal/fediverse/activitypub.go:116:func (aps *ActivityPubServer) SendActivity(targetInbox string, activity *Activity) error {
./internal/fediverse/activitypub.go:75:func (aps *ActivityPubServer) CreateNewsgroupActor(newsgroup *models.Newsgroup) *Actor {
./internal/fediverse/bridge.go:26:func (b *Bridge) Enable() {
./internal/fediverse/bridge.go:33:func (b *Bridge) Disable() {
./internal/fediverse/bridge.go:40:func (b *Bridge) IsEnabled() bool {
./internal/fediverse/bridge.go:46:func (b *Bridge) RegisterNewsgroup(newsgroup *models.Newsgroup) error {
./internal/fediverse/bridge.go:63:func (b *Bridge) BridgeArticle(article *models.Article, newsgroup string) error {
./internal/history/history_config.go:162:func (c *HistoryConfig) ValidateConfig() error {
./internal/history/history_config.go:294:func (h *History) xxxGetHashPrefix(hash string) string {
./internal/history/history_config.go:303:func (h *History) initDatabase() error {
./internal/history/history_config.go:325:func (h *History) openHistoryFile() error {
./internal/history/history.go:1001:func (h *History) xxLookupStorageToken(msgIdItem *MessageIdItem) int {
./internal/history/history.go:1056:func (h *History) CheckNoMoreWorkInHistory() bool {
./internal/history/history.go:1076:func (h *History) SetDatabaseWorkChecker(checker DatabaseWorkChecker) {
./internal/history/history.go:160:func (h *History) bootLookupWorkers() {
./internal/history/history.go:168:func (h *History) LookupWorker(wid int) {
./internal/history/history.go:214:func (h *History) Lookup(msgIdItem *MessageIdItem) (int, error) {
./internal/history/history.go:232:func (h *History) lookupInDatabase(msgIdItem *MessageIdItem) (bool, error) {
./internal/history/history.go:331:func (h *History) GetStats() HistoryStats {
./internal/history/history.go:345:func (h *History) updateStats(fn func(*HistoryStats)) {
./internal/history/history.go:352:func (h *History) Close() error {
./internal/history/history.go:369:func (h *History) writerWorker() {
./internal/history/history.go:463:func (h *History) ServerShutdown() bool {
./internal/history/history.go:476:func (h *History) readHistoryEntryAtOffset(offset int64, msgIdItem *MessageIdItem) (int, error) {
./internal/history/history.go:564:func (h *History) routeHash(msgId string) (int, string, string, error) {
./internal/history/history.go:601:func (h *History) flushPendingBatch() {
./internal/history/history.go:620:func (h *History) processBatch() {
./internal/history/history.go:663:func (h *History) writeBatchToFile() error {
./internal/history/history.go:762:func (h *History) writeBatchToDatabase() error {
./internal/history/history.go:831:func (h *History) writeBatchToHashDB(dbIndex int, entries []*MessageIdItem) error {
./internal/history/history.go:858:func (h *History) executeDBTransaction(dbIndex int, entries []*MessageIdItem) error {
./internal/history/history.go:92:func (h *History) Add(msgIdItem *MessageIdItem) {
./internal/history/history.go:935:func (h *History) processTableInTransaction(tx *sql.Tx, tableName string, hashGroups map[string][]*MessageIdItem) error {
./internal/history/history_L1-cache.go:113:func (c *L1Cache) Close() {
./internal/history/history_L1-cache.go:205:func (l1 *L1CACHE) BootL1Cache() {
./internal/history/history_L1-cache.go:248:func (l1 *L1CACHE) LockL1Cache(hash string, value int) int {
./internal/history/history_L1-cache.go:282:func (l1 *L1CACHE) pqExtend(char string) {
./internal/history/history_L1-cache.go:345:func (l1 *L1CACHE) Set(hash string, char string, value int, flagexpires bool) {
./internal/history/history_L1-cache.go:384:func (l1 *L1CACHE) L1Stats(statskey string) (retval uint64, retmap map[string]uint64) {
./internal/history/history_L1-cache.go:40:func (c *L1Cache) L1Get(hash string) *LookupResponse {
./internal/history/history_L1-cache.go:415:func (pq *L1pqQ) Push(item *L1PQItem) {
./internal/history/history_L1-cache.go:420:func (pq *L1pqQ) Pop() (*L1PQItem, int) {
./internal/history/history_L1-cache.go:436:func (l1 *L1CACHE) pqExpire(char string) {
./internal/history/history_L1-cache.go:52:func (c *L1Cache) Extend(hash string) {
./internal/history/history_L1-cache.go:66:func (c *L1Cache) L1Del(hash string) *LookupResponse {
./internal/history/history_L1-cache.go:80:func (c *L1Cache) Set(hash string, response *LookupResponse) {
./internal/history/history_L1-cache.go:91:func (c *L1Cache) cleanup() {
./internal/history/history_MsgIdItemCache.go:150:func (c *MsgIdItemCache) Stats() (buckets, items, maxChainLength int) {
./internal/history/history_MsgIdItemCache.go:172:func (c *MsgIdItemCache) DetailedStats() (totalBuckets, occupiedBuckets, items, maxChainLength int, loadFactor float64) {
./internal/history/history_MsgIdItemCache.go:203:func (c *MsgIdItemCache) Delete(messageId string) bool {
./internal/history/history_MsgIdItemCache.go:272:func (c *MsgIdItemCache) Clear() {
./internal/history/history_MsgIdItemCache.go:296:func (c *MsgIdItemCache) cleanupMessageIdItem(item *MessageIdItem) {
./internal/history/history_MsgIdItemCache.go:332:func (c *MsgIdItemCache) FNVKey(str string) int {
./internal/history/history_MsgIdItemCache.go:347:func (c *MsgIdItemCache) checkAndResize() {
./internal/history/history_MsgIdItemCache.go:360:func (c *MsgIdItemCache) resize() {
./internal/history/history_MsgIdItemCache.go:399:func (c *MsgIdItemCache) rehashChain(page *MsgIdItemCachePage) {
./internal/history/history_MsgIdItemCache.go:441:func (c *MsgIdItemCache) GetResizeInfo() (bucketCount int, itemCount int, loadFactor float64, isResizing bool) {
./internal/history/history_MsgIdItemCache.go:455:func (c *MsgIdItemCache) GetMsgIdFromCache(newsgroupPtr *string, messageID string) (int64, int64, bool) {
./internal/history/history_MsgIdItemCache.go:493:func (c *MsgIdItemCache) SetThreadingInfo(messageID string, rootArticle int64, isThreadRoot bool) bool {
./internal/history/history_MsgIdItemCache.go:528:func (c *MsgIdItemCache) SetThreadingInfoForGroup(newsgroupPtr *string, messageID string, artNum int64, rootArticle int64, isThreadRoot bool) bool {
./internal/history/history_MsgIdItemCache.go:555:func (c *MsgIdItemCache) AddMsgIdToCache(newsgroupPtr *string, messageID string, articleNum int64) bool {
./internal/history/history_MsgIdItemCache.go:58:func (c *MsgIdItemCache) NewMsgIdItem(messageId string) *MessageIdItem {
./internal/history/history_MsgIdItemCache.go:591:func (c *MsgIdItemCache) CleanExpiredEntries() int {
./internal/history/history_MsgIdItemCache.go:66:func (c *MsgIdItemCache) GetORCreate(messageId string) *MessageIdItem {
./internal/history/history_MsgIdItemCache.go:750:func (c *MsgIdItemCache) StartCleanupRoutine() {
./internal/history/history_MsgIdItemCache.go:796:func (c *MsgIdItemCache) GetOrCreateForGroup(messageID string, newsgroupPtr *string) *MessageIdItem {
./internal/history/history_MsgIdItemCache.go:816:func (c *MsgIdItemCache) HasMessageIDInGroup(messageID string, newsgroupPtr *string) bool {
./internal/history/history_MsgIdItemCache.go:823:func (c *MsgIdItemCache) FindThreadRootInCache(newsgroupPtr *string, references []string) *MessageIdItem {
./internal/history/history_MsgIdItemCache.go:876:func (c *MsgIdItemCache) UpdateThreadRootToTmpCache(newsgroupPtr *string, messageID string, rootArticle int64, isThreadRoot bool) bool {
./internal/history/history_MsgIdItemCache.go:913:func (c *MsgIdItemCache) MsgIdExists(newsgroupPtr *string, messageID string) *MessageIdItem {
./internal/history/history sqlite3 normal.go:67:func (p *SQLite3DB) Close() error {
./internal/history/history sqlite3_sharded.go:103:func (s *SQLite3ShardedDB) Close() error {
./internal/history/history sqlite3_sharded.go:113:func (s *SQLite3ShardedDB) CreateAllTables(useShortHashLen int) error {
./internal/history/history sqlite3_sharded.go:128:func (s *SQLite3ShardedDB) createTablesForDB(dbIndex int, useShortHashLen int) error {
./internal/history/history sqlite3_sharded.go:158:func (s *SQLite3ShardedDB) getTableNamesForDB() []string {
./internal/history/history sqlite3_sharded.go:95:func (s *SQLite3ShardedDB) GetShardedDB(dbIndex int, write bool) (*sql.DB, error) {
./internal/matrix/bridge.go:26:func (b *Bridge) Enable() {
./internal/matrix/bridge.go:33:func (b *Bridge) Disable() {
./internal/matrix/bridge.go:40:func (b *Bridge) IsEnabled() bool {
./internal/matrix/bridge.go:46:func (b *Bridge) RegisterNewsgroup(newsgroup *models.Newsgroup) error {
./internal/matrix/bridge.go:65:func (b *Bridge) BridgeArticle(article *models.Article, newsgroup string) error {
./internal/matrix/client.go:115:func (mc *MatrixClient) ArticleToMessage(article *models.Article) *MatrixMessage {
./internal/matrix/client.go:143:func (mc *MatrixClient) SendArticle(roomID string, article *models.Article) error {
./internal/matrix/client.go:57:func (mc *MatrixClient) CreateRoom(newsgroup *models.Newsgroup) (string, error) {
./internal/matrix/client.go:92:func (mc *MatrixClient) SendMessage(roomID string, message *MatrixMessage) error {
./internal/models/models.go:142:func (a *Article) GetData(what string, group string) string {
./internal/models/models.go:199:func (f *ForumThread) PrintLastActivity() string {
./internal/models/models.go:254:func (n *Newsgroup) PrintLastActivity() string {
./internal/models/models.go:352:func (o *Overview) ReferenceCount() int {
./internal/models/sanitizing.go:185:func (a *Article) PrintSanitized(field string, groupName ...string) template.HTML {
./internal/models/sanitizing.go:455:func (a *Article) StripDangerousHTML() {
./internal/models/sanitizing.go:472:func (o *Overview) PrintSanitized(field string, groupName ...string) template.HTML {
./internal/models/sanitizing.go:538:func (a *Article) GetCleanSubject() string {
./internal/models/sanitizing.go:558:func (o *Overview) GetCleanSubject() string {
./internal/nntp/nntp-article-common.go:306:func (c *ClientConnection) sendArticleContent(result *ArticleRetrievalResult) error {
./internal/nntp/nntp-article-common.go:341:func (c *ClientConnection) sendHeadContent(result *ArticleRetrievalResult) error {
./internal/nntp/nntp-article-common.go:35:func (c *ClientConnection) retrieveArticleCommon(args []string, retrievalType ArticleRetrievalType) error {
./internal/nntp/nntp-article-common.go:362:func (c *ClientConnection) sendBodyContent(result *ArticleRetrievalResult) error {
./internal/nntp/nntp-article-common.go:383:func (c *ClientConnection) sendStatContent(result *ArticleRetrievalResult) error {
./internal/nntp/nntp-article-common.go:78:func (c *ClientConnection) getArticleData(args []string) (*ArticleRetrievalResult, error) {
./internal/nntp/nntp-auth-manager.go:24:func (am *AuthManager) AuthenticateUser(username, password string) (*models.NNTPUser, error) {
./internal/nntp/nntp-auth-manager.go:47:func (am *AuthManager) CheckGroupAccess(user *models.NNTPUser, groupName string) bool {
./internal/nntp/nntp-auth-manager.go:58:func (am *AuthManager) CanPost(user *models.NNTPUser) bool {
./internal/nntp/nntp-auth-manager.go:66:func (am *AuthManager) IsAdmin(user *models.NNTPUser) bool {
./internal/nntp/nntp-auth-manager.go:77:func (am *AuthManager) CheckConnectionLimit(user *models.NNTPUser) bool {
./internal/nntp/nntp-backend-pool.go:112:func (p *Pool) Get() (*BackendConn, error) {
./internal/nntp/nntp-backend-pool.go:184:func (p *Pool) Put(client *BackendConn) error {
./internal/nntp/nntp-backend-pool.go:219:func (p *Pool) CloseConn(client *BackendConn, lock bool) error {
./internal/nntp/nntp-backend-pool.go:240:func (p *Pool) ClosePool() error {
./internal/nntp/nntp-backend-pool.go:266:func (p *Pool) Stats() PoolStats {
./internal/nntp/nntp-backend-pool.go:291:func (p *Pool) createConnection() (*BackendConn, error) {
./internal/nntp/nntp-backend-pool.go:305:func (p *Pool) isConnectionValid(client *BackendConn) bool {
./internal/nntp/nntp-backend-pool.go:324:func (p *Pool) Cleanup() {
./internal/nntp/nntp-backend-pool.go:373:func (p *Pool) StartCleanupWorker(interval time.Duration) {
./internal/nntp/nntp-backend-pool.go:39:func (p *Pool) XOver(group string, start, end int64, enforceLimit bool) ([]OverviewLine, error) {
./internal/nntp/nntp-backend-pool.go:59:func (p *Pool) XHdr(group string, header string, start, end int64) ([]HeaderLine, error) {
./internal/nntp/nntp-backend-pool.go:69:func (p *Pool) GetArticle(messageID string) (*models.Article, error) {
./internal/nntp/nntp-backend-pool.go:93:func (p *Pool) SelectGroup(group string) (*GroupInfo, error) {
./internal/nntp/nntp-cache-local.go:102:func (c *CacheMessageIDNumtoGroup) Del(messageID, group string) {
./internal/nntp/nntp-cache-local.go:115:func (c *CacheMessageIDNumtoGroup) Clear(messageID string) {
./internal/nntp/nntp-cache-local.go:122:func (c *CacheMessageIDNumtoGroup) CleanupCron() {
./internal/nntp/nntp-cache-local.go:132:func (c *CacheMessageIDNumtoGroup) Cleanup() {
./internal/nntp/nntp-cache-local.go:17:func (lc *Local430) CronLocal430() {
./internal/nntp/nntp-cache-local.go:25:func (lc *Local430) Check(msgIdItem *history.MessageIdItem) bool {
./internal/nntp/nntp-cache-local.go:34:func (lc *Local430) Add(msgIdItem *history.MessageIdItem) bool {
./internal/nntp/nntp-cache-local.go:43:func (lc *Local430) Cleanup() {
./internal/nntp/nntp-cache-local.go:75:func (c *CacheMessageIDNumtoGroup) Get(messageID, group string) (int64, bool) {
./internal/nntp/nntp-cache-local.go:88:func (c *CacheMessageIDNumtoGroup) Set(messageID, group string, articleNum int64) {
./internal/nntp/nntp-client-commands.go:116:func (c *BackendConn) GetHead(messageID string) (*models.Article, error) {
./internal/nntp/nntp-client-commands.go:172:func (c *BackendConn) GetBody(messageID string) ([]byte, error) {
./internal/nntp/nntp-client-commands.go:220:func (c *BackendConn) ListGroups() ([]GroupInfo, error) {
./internal/nntp/nntp-client-commands.go:269:func (c *BackendConn) ListGroupsLimited(maxGroups int) ([]GroupInfo, error) {
./internal/nntp/nntp-client-commands.go:30:func (c *BackendConn) StatArticle(messageID string) (bool, error) {
./internal/nntp/nntp-client-commands.go:349:func (c *BackendConn) SelectGroup(groupName string) (*GroupInfo, error) {
./internal/nntp/nntp-client-commands.go:416:func (c *BackendConn) XOver(groupName string, start, end int64, enforceLimit bool) ([]OverviewLine, error) {
./internal/nntp/nntp-client-commands.go:484:func (c *BackendConn) XHdr(groupName, field string, start, end int64) ([]HeaderLine, error) {
./internal/nntp/nntp-client-commands.go:545:func (c *BackendConn) ListGroup(groupName string, start, end int64) ([]int64, error) {
./internal/nntp/nntp-client-commands.go:604:func (c *BackendConn) readMultilineResponse(src string) ([]string, error) {
./internal/nntp/nntp-client-commands.go:64:func (c *BackendConn) GetArticle(messageID string) (*models.Article, error) {
./internal/nntp/nntp-client-commands.go:756:func (c *BackendConn) parseGroupLine(line string) (GroupInfo, error) {
./internal/nntp/nntp-client-commands.go:789:func (c *BackendConn) parseOverviewLine(line string) (OverviewLine, error) {
./internal/nntp/nntp-client-commands.go:816:func (c *BackendConn) parseHeaderLine(line string) (HeaderLine, error) {
./internal/nntp/nntp-client.go:137:func (c *BackendConn) Connect() error {
./internal/nntp/nntp-client.go:213:func (c *BackendConn) authenticate() error {
./internal/nntp/nntp-client.go:255:func (c *BackendConn) CloseFromPoolOnly() error {
./internal/nntp/nntp-client.go:285:func (c *BackendConn) xSetReadDeadline(t time.Time) error {
./internal/nntp/nntp-client.go:295:func (c *BackendConn) xSetWriteDeadline(t time.Time) error {
./internal/nntp/nntp-client.go:304:func (c *BackendConn) UpdateLastUsed() {
./internal/nntp/nntp-cmd-article.go:4:func (c *ClientConnection) handleArticle(args []string) error {
./internal/nntp/nntp-cmd-auth.go:9:func (c *ClientConnection) handleAuthInfo(args []string) error {
./internal/nntp/nntp-cmd-basic.go:15:func (c *ClientConnection) handleMode(args []string) error {
./internal/nntp/nntp-cmd-basic.go:30:func (c *ClientConnection) handleHelp() error {
./internal/nntp/nntp-cmd-basic.go:53:func (c *ClientConnection) handleQuit() error {
./internal/nntp/nntp-cmd-basic.go:59:func (c *ClientConnection) Close() {
./internal/nntp/nntp-cmd-basic.go:9:func (c *ClientConnection) handleCapabilities() error {
./internal/nntp/nntp-cmd-body.go:4:func (c *ClientConnection) handleBody(args []string) error {
./internal/nntp/nntp-cmd-group.go:37:func (c *ClientConnection) handleListGroup(args []string) error {
./internal/nntp/nntp-cmd-group.go:9:func (c *ClientConnection) handleGroup(args []string) error {
./internal/nntp/nntp-cmd-head.go:4:func (c *ClientConnection) handleHead(args []string) error {
./internal/nntp/nntp-cmd-helpers.go:12:func (c *ClientConnection) rateLimitOnError() {
./internal/nntp/nntp-cmd-helpers.go:19:func (c *ClientConnection) parseArticleHeadersFull(article *models.Article) []string {
./internal/nntp/nntp-cmd-helpers.go:24:func (c *ClientConnection) parseArticleHeadersShort(article *models.Article) []string {
./internal/nntp/nntp-cmd-helpers.go:48:func (c *ClientConnection) parseArticleBody(article *models.Article) []string {
./internal/nntp/nntp-cmd-helpers.go:67:func (c *ClientConnection) formatOverviewLine(overview *models.Overview) string {
./internal/nntp/nntp-cmd-list.go:26:func (c *ClientConnection) handleListActive() error {
./internal/nntp/nntp-cmd-list.go:46:func (c *ClientConnection) handleListNewsgroups() error {
./internal/nntp/nntp-cmd-list.go:9:func (c *ClientConnection) handleList(args []string) error {
./internal/nntp/nntp-cmd-posting.go:108:func (c *ClientConnection) handleTakeThis(args []string) error {
./internal/nntp/nntp-cmd-posting.go:13:func (c *ClientConnection) handlePost() error {
./internal/nntp/nntp-cmd-posting.go:173:func (c *ClientConnection) readArticleData() (*models.Article, error) {
./internal/nntp/nntp-cmd-posting.go:49:func (c *ClientConnection) handleIHave(args []string) error {
./internal/nntp/nntp-cmd-stat.go:4:func (c *ClientConnection) handleStat(args []string) error {
./internal/nntp/nntp-cmd-xhdr.go:10:func (c *ClientConnection) handleXHdr(args []string) error {
./internal/nntp/nntp-cmd-xover.go:9:func (c *ClientConnection) handleXOver(args []string) error {
./internal/nntp/nntp-peering.go:196:func (pm *PeeringManager) LoadConfiguration() error {
./internal/nntp/nntp-peering.go:208:func (pm *PeeringManager) ValidatePeerConfig(peer *Peer) error {
./internal/nntp/nntp-peering.go:248:func (pm *PeeringManager) AddPeer(peer *Peer) error {
./internal/nntp/nntp-peering.go:271:func (pm *PeeringManager) GetPeer(hostname string) (*Peer, bool) {
./internal/nntp/nntp-peering.go:280:func (pm *PeeringManager) CheckConnectionACL(conn net.Conn) (*Peer, bool) {
./internal/nntp/nntp-peering.go:322:func (pm *PeeringManager) matchesCIDR(ipAddress, cidr string) bool {
./internal/nntp/nntp-peering.go:341:func (pm *PeeringManager) reverseDNSLookup(ipAddress string) (string, bool) {
./internal/nntp/nntp-peering.go:389:func (pm *PeeringManager) enhancedReverseDNSLookup(remoteAddr string) (string, bool) {
./internal/nntp/nntp-peering.go:413:func (pm *PeeringManager) validateHostnameForward(hostname string, expectedAddr string) bool {
./internal/nntp/nntp-peering.go:435:func (pm *PeeringManager) updateStats() {
./internal/nntp/nntp-peering.go:455:func (pm *PeeringManager) GetStats() PeeringStats {
./internal/nntp/nntp-peering.go:464:func (pm *PeeringManager) GetAllPeers() []Peer {
./internal/nntp/nntp-peering.go:474:func (pm *PeeringManager) Close() error {
./internal/nntp/nntp-peering.go:483:func (pm *PeeringManager) ApplyDefaultExclusions(peer *Peer) {
./internal/nntp/nntp-peering.go:518:func (pm *PeeringManager) ApplyDefaultBinaryExclusions(peer *Peer) {
./internal/nntp/nntp-peering.go:537:func (pm *PeeringManager) CreateDefaultPeer(hostname, pathHostname string) *Peer {
./internal/nntp/nntp-server-cliconns.go:102:func (c *ClientConnection) handleCommand(line string) error {
./internal/nntp/nntp-server-cliconns.go:156:func (c *ClientConnection) sendResponse(code int, message string) error {
./internal/nntp/nntp-server-cliconns.go:161:func (c *ClientConnection) sendLine(line string) error {
./internal/nntp/nntp-server-cliconns.go:172:func (c *ClientConnection) sendMultilineResponse(code int, message string, lines []string) error {
./internal/nntp/nntp-server-cliconns.go:196:func (c *ClientConnection) getServerCapabilities() []string {
./internal/nntp/nntp-server-cliconns.go:216:func (c *ClientConnection) RemoteAddr() net.Addr {
./internal/nntp/nntp-server-cliconns.go:54:func (c *ClientConnection) UpdateDeadlines() {
./internal/nntp/nntp-server-cliconns.go:62:func (c *ClientConnection) Handle() error {
./internal/nntp/nntp-server-cliconns.go:93:func (c *ClientConnection) sendWelcome() error {
./internal/nntp/nntp-server.go:125:func (s *NNTPServer) serve(listener net.Listener, isTLS bool) {
./internal/nntp/nntp-server.go:159:func (s *NNTPServer) handleConnection(conn net.Conn, isTLS bool) {
./internal/nntp/nntp-server.go:174:func (s *NNTPServer) Stop() error {
./internal/nntp/nntp-server.go:215:func (s *NNTPServer) IsRunning() bool {
./internal/nntp/nntp-server.go:76:func (s *NNTPServer) Start() error {
./internal/nntp/nntp-server-statistics.go:104:func (s *ServerStats) GetUptime() time.Duration {
./internal/nntp/nntp-server-statistics.go:111:func (s *ServerStats) Reset() {
./internal/nntp/nntp-server-statistics.go:28:func (s *ServerStats) ConnectionStarted() {
./internal/nntp/nntp-server-statistics.go:36:func (s *ServerStats) ConnectionEnded() {
./internal/nntp/nntp-server-statistics.go:43:func (s *ServerStats) GetActiveConnections() int {
./internal/nntp/nntp-server-statistics.go:50:func (s *ServerStats) GetTotalConnections() int64 {
./internal/nntp/nntp-server-statistics.go:57:func (s *ServerStats) CommandExecuted(command string) {
./internal/nntp/nntp-server-statistics.go:64:func (s *ServerStats) GetCommandCount(command string) int64 {
./internal/nntp/nntp-server-statistics.go:71:func (s *ServerStats) GetAllCommandCounts() map[string]int64 {
./internal/nntp/nntp-server-statistics.go:83:func (s *ServerStats) AuthSuccess() {
./internal/nntp/nntp-server-statistics.go:90:func (s *ServerStats) AuthFailure() {
./internal/nntp/nntp-server-statistics.go:97:func (s *ServerStats) GetAuthStats() (successes, failures int64) {
./internal/processor/analyze.go:117:func (proc *Processor) AnalyzeGroup(groupName string, options *AnalyzeOptions) (*GroupAnalysis, error) {
./internal/processor/analyze.go:177:func (proc *Processor) analyzeFromRemote(groupInfo *nntp.GroupInfo, analysis *GroupAnalysis, options *AnalyzeOptions) (*GroupAnalysis, error) {
./internal/processor/analyze.go:291:func (proc *Processor) analyzeFromCache(cacheFile string, analysis *GroupAnalysis, options *AnalyzeOptions) (*GroupAnalysis, error) {
./internal/processor/analyze.go:381:func (proc *Processor) FindArticlesByDateRange(groupName string, startDate, endDate time.Time) (*DateRangeResult, error) {
./internal/processor/analyze.go:401:func (proc *Processor) findDateRangeInCache(cacheFile string, startDate, endDate time.Time) (*DateRangeResult, error) {
./internal/processor/analyze.go:484:func (proc *Processor) getCacheFilePath(providerName string, groupName string) string {
./internal/processor/analyze.go:493:func (proc *Processor) cacheFileExists(cacheFile string) bool {
./internal/processor/analyze.go:499:func (proc *Processor) GetCachedMessageIDs(groupName string, startArticle, endArticle int64) ([]nntp.HeaderLine, error) {
./internal/processor/analyze.go:576:func (proc *Processor) ClearCache(groupName string) error {
./internal/processor/analyze.go:596:func (proc *Processor) GetCacheStats(groupName string) (*GroupAnalysis, error) {
./internal/processor/analyze.go:60:func (stats *ArticleSizeStats) AddArticleSize(bytes int64) {
./internal/processor/analyze.go:621:func (proc *Processor) ValidateCacheIntegrity(groupName string) error {
./internal/processor/analyze.go:679:func (proc *Processor) GetArticleCountByDateRange(groupName string, startDate, endDate time.Time) (int64, error) {
./internal/processor/analyze.go:729:func (analysis *GroupAnalysis) ExportAnalysisToJSON() ([]byte, error) {
./internal/processor/analyze.go:761:func (analysis *GroupAnalysis) ExportAnalysisToCSV() string {
./internal/processor/analyze.go:783:func (proc *Processor) AnalyzeMode(testGrp string, forceRefresh bool, maxAnalyzeArticles int64,
./internal/processor/analyze.go:85:func (stats *ArticleSizeStats) PrintSizeDistribution() {
./internal/processor/bridges.go:106:func (bm *BridgeManager) Close() {
./internal/processor/bridges.go:56:func (bm *BridgeManager) IsAnyBridgeEnabled() bool {
./internal/processor/bridges.go:60:func (bm *BridgeManager) RegisterNewsgroup(newsgroup *models.Newsgroup) error {
./internal/processor/bridges.go:86:func (bm *BridgeManager) BridgeArticle(article *models.Article, newsgroup string) {
./internal/processor/counter.go:23:func (*Counter) GetReset(group string) int64 {
./internal/processor/counter.go:35:func (*Counter) GetResetAll() map[string]int64 {
./internal/processor/counter.go:47:func (*Counter) Add(group string, value int64) {
./internal/processor/counter.go:57:func (*Counter) Increment(group string) {
./internal/processor/interface.go:10:func (proc *Processor) IsNewsGroupInSectionsDB(name *string) bool {
./internal/processor/interface.go:5:func (proc *Processor) MsgIdExists(group *string, messageID string) bool {
./internal/processor/proc_DLArt.go:22:func (proc *Processor) DownloadArticles(newsgroup string, ignoreInitialTinyGroups int64) error {
./internal/processor/proc_DLArt.go:351:func (proc *Processor) FindStartArticleByDate(groupName string, targetDate time.Time) (int64, error) {
./internal/processor/proc_DLArt.go:412:func (proc *Processor) DownloadArticlesFromDate(groupName string, startDate time.Time, ignoreInitialTinyGroups int64) error {
./internal/processor/proc_DLArtOV.go:12:func (proc *Processor) DownloadArticlesViaOverview(groupName string) error {
./internal/processor/proc_DLXHDR.go:6:func (proc *Processor) GetXHDR(groupName string, header string, start, end int64) ([]nntp.HeaderLine, error) {
./internal/processor/processor.go:134:func (proc *Processor) CheckNoMoreWorkInHistory() bool {
./internal/processor/processor.go:139:func (proc *Processor) AddProcessedArticleToHistory(msgIdItem *history.MessageIdItem, newsgroupPtr *string, articleNumber int64) {
./internal/processor/processor.go:172://func (proc *Processor) AddMsgIdToTmpCache(group string, msgIdItem *history.MessageIdItem, articleNumber int64) {
./internal/processor/processor.go:178:func (proc *Processor) FindThreadRootInCache(newsgroupPtr *string, refs []string) *database.MsgIdTmpCacheItem {
./internal/processor/processor.go:205:func (proc *Processor) GetHistoryStats() history.HistoryStats {
./internal/processor/processor.go:213:func (proc *Processor) Close() error {
./internal/processor/processor.go:229:func (proc *Processor) WaitForBatchCompletion() {
./internal/processor/processor.go:264:func (proc *Processor) Lookup(msgIdItem *history.MessageIdItem) (int, error) {
./internal/processor/processor.go:270:func (proc *Processor) AddArticleToHistory(article *nntp.Article, newsgroup string) {
./internal/processor/processor.go:275:func (proc *Processor) ProcessIncomingArticle(article *models.Article) (int, error) {
./internal/processor/processor.go:295:func (proc *Processor) EnableBridges(config *BridgeConfig) {
./internal/processor/processor.go:307:func (proc *Processor) DisableBridges() {
./internal/processor/proc_ImportOV.go:13:func (proc *Processor) ImportOverview(groupName string) error {
./internal/processor/proc_MsgIDtmpCache.go:105://func (c *MsgTmpCache) Clear() {
./internal/processor/proc_MsgIDtmpCache.go:112://func (c *MsgTmpCache) UpdateThreadRootToTmpCache(group string, messageID string, rootArticle int64, isThreadRoot bool) bool {
./internal/processor/proc_MsgIDtmpCache.go:130://func (c *MsgTmpCache) AddThreadRootToTmpCache(group string, messageID string, articleNum int64) bool {
./internal/processor/proc_MsgIDtmpCache.go:149://func (c *MsgTmpCache) FindThreadRootInCache(group string, references []string) *MsgIdTmpCacheItem {
./internal/processor/proc_MsgIDtmpCache.go:28://func (c *MsgTmpCache) CronClean() {
./internal/processor/proc_MsgIDtmpCache.go:67://func (c *MsgTmpCache) AddMsgIdToTmpCache(group string, messageID string, articleNum int64) bool {
./internal/processor/proc_MsgIDtmpCache.go:87://func (c *MsgTmpCache) MsgIdExists(group string, messageID string) *MsgIdTmpCacheItem {
./internal/processor/proc-utils.go:437:func (proc *Processor) extractGroupsFromHeaders(msgID, groupsline string) []string {
./internal/processor/rslight_articles.go:12:func (leg *LegacyImporter) QuickOpenToGetNewsgroup(sqlitePath string) (string, error) {
./internal/processor/rslight_articles.go:45:func (leg *LegacyImporter) ImportSQLiteArticles(sqlitePath string) error {
./internal/processor/rslight.go:143:func (leg *LegacyImporter) parseMenuConf() ([]MenuConfEntry, error) {
./internal/processor/rslight.go:196:func (leg *LegacyImporter) importSectionGroups(sectionID int, sectionName string) error {
./internal/processor/rslight.go:282:func (leg *LegacyImporter) insertSection(section *models.Section) (int, error) {
./internal/processor/rslight.go:302:func (leg *LegacyImporter) insertSectionGroup(sectionGroup *models.SectionGroup) error {
./internal/processor/rslight.go:330:func (leg *LegacyImporter) importLegacyArticle(legacyArticle *LegacyArticle) error {
./internal/processor/rslight.go:375:func (leg *LegacyImporter) ImportAllSQLiteDatabases(sqliteDir string, threads int) error {
./internal/processor/rslight.go:507:func (leg *LegacyImporter) GetSectionsSummary() error {
./internal/processor/rslight.go:582:func (leg *LegacyImporter) insertNewsgroupIfNotExists(name, description string) error {
./internal/processor/rslight.go:86:func (leg *LegacyImporter) Close() error {
./internal/processor/rslight.go:94:func (leg *LegacyImporter) ImportSections() error {
./internal/processor/threading.go:78:func (proc *Processor) resetCaseWriteOnError(msgIdItem *history.MessageIdItem, bulkmode bool) {
./internal/processor/threading.go:87:func (proc *Processor) processArticle(art *models.Article, legacyNewsgroup string, bulkmode bool) (int, error) {
./internal/web/web_admin_apitokens.go:140:func (s *WebServer) adminDeleteAPIToken(c *gin.Context) {
./internal/web/web_admin_apitokens.go:14:func (s *WebServer) countEnabledAPITokens(tokens []*database.APIToken) int {
./internal/web/web_admin_apitokens.go:183:func (s *WebServer) adminCleanupExpiredTokens(c *gin.Context) {
./internal/web/web_admin_apitokens.go:25:func (s *WebServer) adminCreateAPIToken(c *gin.Context) {
./internal/web/web_admin_apitokens.go:89:func (s *WebServer) adminToggleAPIToken(c *gin.Context) {
./internal/web/web_admin_cache.go:11:func (s *WebServer) adminClearCache(c *gin.Context) {
./internal/web/web_admin.go:57:func (s *WebServer) getUptime() string {
./internal/web/web_admin_newsgroups.go:114:func (s *WebServer) adminUpdateNewsgroup(c *gin.Context) {
./internal/web/web_admin_newsgroups.go:202:func (s *WebServer) adminDeleteNewsgroup(c *gin.Context) {
./internal/web/web_admin_newsgroups.go:238:func (s *WebServer) adminAssignNewsgroupSection(c *gin.Context) {
./internal/web/web_admin_newsgroups.go:27:func (s *WebServer) adminCreateNewsgroup(c *gin.Context) {
./internal/web/web_admin_newsgroups.go:329:func (s *WebServer) adminToggleNewsgroup(c *gin.Context) {
./internal/web/web_admin_newsgroups.go:381:func (s *WebServer) adminBulkEnableNewsgroups(c *gin.Context) {
./internal/web/web_admin_newsgroups.go:386:func (s *WebServer) adminBulkDisableNewsgroups(c *gin.Context) {
./internal/web/web_admin_newsgroups.go:391:func (s *WebServer) adminBulkDeleteNewsgroups(c *gin.Context) {
./internal/web/web_admin_newsgroups.go:432:func (s *WebServer) handleBulkNewsgroupAction(c *gin.Context, activeStatus bool, actionName string) {
./internal/web/web_admin_nntp.go:140:func (s *WebServer) adminUpdateNNTPUser(c *gin.Context) {
./internal/web/web_admin_nntp.go:228:func (s *WebServer) adminDeleteNNTPUser(c *gin.Context) {
./internal/web/web_admin_nntp.go:272:func (s *WebServer) adminToggleNNTPUser(c *gin.Context) {
./internal/web/web_admin_nntp.go:27:func (s *WebServer) countActiveNNTPUsers(nntpUsers []*models.NNTPUser) int {
./internal/web/web_admin_nntp.go:334:func (s *WebServer) adminEnableNNTPUser(c *gin.Context) {
./internal/web/web_admin_nntp.go:38:func (s *WebServer) countPostingNNTPUsers(nntpUsers []*models.NNTPUser) int {
./internal/web/web_admin_nntp.go:49:func (s *WebServer) adminCreateNNTPUser(c *gin.Context) {
./internal/web/web_admin_ollama.go:100:func (s *WebServer) adminUpdateAIModel(c *gin.Context) {
./internal/web/web_admin_ollama.go:175:func (s *WebServer) adminDeleteAIModel(c *gin.Context) {
./internal/web/web_admin_ollama.go:218:func (s *WebServer) adminSyncOllamaModels(c *gin.Context) {
./internal/web/web_admin_ollama.go:33:func (s *WebServer) adminCreateAIModel(c *gin.Context) {
./internal/web/web_adminPage.go:15:func (s *WebServer) adminPage(c *gin.Context) {
./internal/web/web_admin_provider.go:137:func (s *WebServer) adminUpdateProvider(c *gin.Context) {
./internal/web/web_admin_provider.go:15:func (s *WebServer) adminCreateProvider(c *gin.Context) {
./internal/web/web_admin_provider.go:289:func (s *WebServer) adminDeleteProvider(c *gin.Context) {
./internal/web/web_admin_registration.go:10:func (s *WebServer) adminEnableRegistration(c *gin.Context) {
./internal/web/web_admin_registration.go:43:func (s *WebServer) adminDisableRegistration(c *gin.Context) {
./internal/web/web_admin_sections.go:101:func (s *WebServer) UpdateSectionHandler(c *gin.Context) {
./internal/web/web_admin_sections.go:16:func (s *WebServer) SectionsHandler(c *gin.Context) {
./internal/web/web_admin_sections.go:189:func (s *WebServer) DeleteSectionHandler(c *gin.Context) {
./internal/web/web_admin_sections.go:22:func (s *WebServer) CreateSectionHandler(c *gin.Context) {
./internal/web/web_admin_sections.go:240:func (s *WebServer) AssignNewsgroupHandler(c *gin.Context) {
./internal/web/web_admin_sections.go:381:func (s *WebServer) UnassignNewsgroupHandler(c *gin.Context) {
./internal/web/web_admin_sitenews.go:152:func (s *WebServer) adminDeleteSiteNews(c *gin.Context) {
./internal/web/web_admin_sitenews.go:16:func (s *WebServer) adminCreateSiteNews(c *gin.Context) {
./internal/web/web_admin_sitenews.go:190:func (s *WebServer) adminToggleSiteNewsVisibility(c *gin.Context) {
./internal/web/web_admin_sitenews.go:76:func (s *WebServer) adminUpdateSiteNews(c *gin.Context) {
./internal/web/web_admin_userfuncs.go:128:func (s *WebServer) adminUpdateUser(c *gin.Context) {
./internal/web/web_admin_userfuncs.go:15:func (s *WebServer) countAdminUsers(users []*models.User) int {
./internal/web/web_admin_userfuncs.go:177:func (s *WebServer) adminDeleteUser(c *gin.Context) {
./internal/web/web_admin_userfuncs.go:221:func (s *WebServer) isAdmin(user *models.User) bool {
./internal/web/web_admin_userfuncs.go:26:func (s *WebServer) countActiveSessions() int {
./internal/web/web_admin_userfuncs.go:32:func (s *WebServer) adminCreateUser(c *gin.Context) {
./internal/web/web_aichatPage.go:153:func (s *WebServer) aichatSend(c *gin.Context) {
./internal/web/web_aichatPage.go:294:func (s *WebServer) aichatModels(c *gin.Context) {
./internal/web/web_aichatPage.go:325:func (s *WebServer) aichatLoadHistory(c *gin.Context) {
./internal/web/web_aichatPage.go:373:func (s *WebServer) aichatClearHistory(c *gin.Context) {
./internal/web/web_aichatPage.go:430:func (s *WebServer) aichatGetCounts(c *gin.Context) {
./internal/web/web_aichatPage.go:459:func (s *WebServer) renderChatError(c *gin.Context, title, message string) {
./internal/web/web_aichatPage.go:82:func (s *WebServer) aichatPage(c *gin.Context) {
./internal/web/web_apiHandlers.go:163:func (s *WebServer) getArticle(c *gin.Context) {
./internal/web/web_apiHandlers.go:188:func (s *WebServer) getArticleByMessageId(c *gin.Context) {
./internal/web/web_apiHandlers.go:18:// - func (s *WebServer) listGroups(c *gin.Context) (line ~313)
./internal/web/web_apiHandlers.go:207:func (s *WebServer) getGroupThreads(c *gin.Context) {
./internal/web/web_apiHandlers.go:20:// - func (s *WebServer) getGroupOverview(c *gin.Context) (line ~354)
./internal/web/web_apiHandlers.go:226:func (s *WebServer) getStats(c *gin.Context) {
./internal/web/web_apiHandlers.go:22:// - func (s *WebServer) getArticle(c *gin.Context) (line ~403)
./internal/web/web_apiHandlers.go:24:// - func (s *WebServer) getArticleByMessageId(c *gin.Context) (line ~428)
./internal/web/web_apiHandlers.go:26:// - func (s *WebServer) getGroupThreads(c *gin.Context) (line ~447)
./internal/web/web_apiHandlers.go:28:// - func (s *WebServer) getStats(c *gin.Context) (line ~466)
./internal/web/web_apiHandlers.go:297:func (s *WebServer) getArticlePreview(c *gin.Context) {
./internal/web/web_apiHandlers.go:33:func (s *WebServer) listGroups(c *gin.Context) {
./internal/web/web_apiHandlers.go:74:func (s *WebServer) getGroupOverview(c *gin.Context) {
./internal/web/web_apitokens.go:120:func (s *WebServer) disableAPITokenHandler(c *gin.Context) {
./internal/web/web_apitokens.go:14:func (s *WebServer) APIAuthRequired() gin.HandlerFunc {
./internal/web/web_apitokens.go:153:func (s *WebServer) enableAPITokenHandler(c *gin.Context) {
./internal/web/web_apitokens.go:185:func (s *WebServer) deleteAPITokenHandler(c *gin.Context) {
./internal/web/web_apitokens.go:217:func (s *WebServer) cleanupExpiredTokensHandler(c *gin.Context) {
./internal/web/web_apitokens.go:54:func (s *WebServer) createAPITokenHandler(c *gin.Context) {
./internal/web/web_apitokens.go:89:func (s *WebServer) listAPITokensHandler(c *gin.Context) {
./internal/web/web_articlePage.go:16://   - func (s *WebServer) articlePage(c *gin.Context) (line ~671)
./internal/web/web_articlePage.go:18://   - func (s *WebServer) articleByMessageIdPage(c *gin.Context) (line ~739)
./internal/web/web_articlePage.go:23:func (s *WebServer) articlePage(c *gin.Context) {
./internal/web/web_articlePage.go:95:func (s *WebServer) articleByMessageIdPage(c *gin.Context) {
./internal/web/web_auth.go:110:func (s *WebServer) WebAdminRequired() gin.HandlerFunc {
./internal/web/web_auth.go:147:func (s *WebServer) getWebSession(c *gin.Context) *SessionData {
./internal/web/web_auth.go:176:func (s *WebServer) createWebSession(c *gin.Context, userID int64) error {
./internal/web/web_auth.go:252:func (s *WebServer) setSessionCookie(c *gin.Context, sessionID string) {
./internal/web/web_auth.go:271:func (s *WebServer) clearSessionCookie(c *gin.Context) {
./internal/web/web_auth.go:72:func (s *SessionData) SetError(msg string) {
./internal/web/web_auth.go:77:func (s *SessionData) SetSuccess(msg string) {
./internal/web/web_auth.go:82:func (s *SessionData) GetSuccess() string {
./internal/web/web_auth.go:88:func (s *SessionData) GetError() string {
./internal/web/web_auth.go:94:func (s *WebServer) WebAuthRequired() gin.HandlerFunc {
./internal/web/webgroupPage_admin.go:16:func (s *WebServer) decrementSpam(c *gin.Context) {
./internal/web/webgroupPage_admin.go:91:func (s *WebServer) unhideArticle(c *gin.Context) {
./internal/web/webgroupPage.go:129:func (s *WebServer) incrementSpam(c *gin.Context) {
./internal/web/webgroupPage.go:19://   - func (s *WebServer) groupPage(c *gin.Context) (line ~598)
./internal/web/webgroupPage.go:222:func (s *WebServer) incrementHide(c *gin.Context) {
./internal/web/webgroupPage.go:23:func (s *WebServer) groupPage(c *gin.Context) {
./internal/web/web_groupsPage.go:16:// - func (s *WebServer) groupsPage(c *gin.Context) (line ~553)
./internal/web/web_groupsPage.go:21:func (s *WebServer) groupsPage(c *gin.Context) {
./internal/web/web_groupThreadsPage.go:15:func (s *WebServer) groupThreadsPage(c *gin.Context) {
./internal/web/web_helpPage.go:14://   - func (s *WebServer) helpPage(c *gin.Context) (line ~880)
./internal/web/web_helpPage.go:18:func (s *WebServer) helpPage(c *gin.Context) {
./internal/web/web_hierarchiesPage.go:127:func (s *WebServer) adminUpdateHierarchies(c *gin.Context) {
./internal/web/web_hierarchiesPage.go:16:func (s *WebServer) hierarchiesPage(c *gin.Context) {
./internal/web/web_hierarchiesPage.go:68:func (s *WebServer) hierarchyGroupsPage(c *gin.Context) {
./internal/web/web_homePage.go:14:// - func (s *WebServer) homePage(c *gin.Context) (line ~538)
./internal/web/web_homePage.go:19:func (s *WebServer) homePage(c *gin.Context) {
./internal/web/web_login.go:127:func (s *WebServer) logout(c *gin.Context) {
./internal/web/web_login.go:141:func (s *WebServer) renderLoginError(c *gin.Context, errorMsg, redirectURL string) {
./internal/web/web_login.go:20:func (s *WebServer) loginPage(c *gin.Context) {
./internal/web/web_login.go:57:func (s *WebServer) loginSubmit(c *gin.Context) {
./internal/web/web_newsPage.go:17:func (s *WebServer) newsPage(c *gin.Context) {
./internal/web/web_profile.go:21:func (s *WebServer) profilePage(c *gin.Context) {
./internal/web/web_profile.go:53:func (s *WebServer) profileUpdate(c *gin.Context) {
./internal/web/web_registerPage.go:145:func (s *WebServer) createUser(username, email, passwordHash, displayName string) (*models.User, error) {
./internal/web/web_registerPage.go:177:func (s *WebServer) renderRegisterError(c *gin.Context, errorMsg, username, email string) {
./internal/web/web_registerPage.go:23:func (s *WebServer) registerPage(c *gin.Context) {
./internal/web/web_registerPage.go:54:func (s *WebServer) registerSubmit(c *gin.Context) {
./internal/web/web_searchPage.go:17://   - func (s *WebServer) searchPage(c *gin.Context) (line ~784)
./internal/web/web_searchPage.go:22:func (s *WebServer) searchPage(c *gin.Context) {
./internal/web/web_sectionsPage.go:149:func (s *WebServer) sectionGroupPage(c *gin.Context) {
./internal/web/web_sectionsPage.go:17://   - func (s *WebServer) sectionsPage(c *gin.Context) (line ~893)
./internal/web/web_sectionsPage.go:19://   - func (s *WebServer) sectionPage(c *gin.Context) (line ~932)
./internal/web/web_sectionsPage.go:21://   - func (s *WebServer) sectionGroupPage(c *gin.Context) (line ~1027)
./internal/web/web_sectionsPage.go:23://   - func (s *WebServer) sectionArticlePage(c *gin.Context) (line ~1123)
./internal/web/web_sectionsPage.go:25://   - func (s *WebServer) sectionArticleByMessageIdPage(c *gin.Context) (line ~1204)
./internal/web/web_sectionsPage.go:276:func (s *WebServer) sectionArticlePage(c *gin.Context) {
./internal/web/web_sectionsPage.go:33:func (s *WebServer) sectionsPage(c *gin.Context) {
./internal/web/web_sectionsPage.go:352:func (s *WebServer) sectionArticleByMessageIdPage(c *gin.Context) {
./internal/web/web_sectionsPage.go:62:func (s *WebServer) sectionPage(c *gin.Context) {
./internal/web/webserver_core_routes.go:224:func (s *WebServer) setupRoutes() {
./internal/web/webserver_core_routes.go:399:func (s *WebServer) Start() error {
./internal/web/webserver_core_routes.go:415:func (s *WebServer) BotDetectionMiddleware() gin.HandlerFunc {
./internal/web/webserver_core_routes.go:434:func (s *WebServer) ReverseProxyMiddleware() gin.HandlerFunc {
./internal/web/webserver_core_routes.go:465:func (s *WebServer) ApacheLogFormat() gin.HandlerFunc {
./internal/web/webserver_core_routes.go:482:func (s *WebServer) loadSectionsCache() {
./internal/web/webserver_core_routes.go:499:func (s *WebServer) refreshSectionsCache() {
./internal/web/webserver_core_routes.go:504:func (s *WebServer) isValidSection(sectionName string) bool {
./internal/web/webserver_core_routes.go:512:func (s *WebServer) sectionValidationMiddleware() gin.HandlerFunc {
./internal/web/web_session_cleanup.go:9:func (s *WebServer) StartSessionCleanup() {
./internal/web/web_statsPage.go:14://   - func (s *WebServer) statsPage(c *gin.Context) (line ~857)
./internal/web/web_statsPage.go:18:func (s *WebServer) statsPage(c *gin.Context) {
./internal/web/web_threadPage.go:17://   - func (s *WebServer) singleThreadPage(c *gin.Context) (line ~1394)
./internal/web/web_threadPage.go:25:func (s *WebServer) singleThreadPage(c *gin.Context) {
./internal/web/web_threadTreePage.go:167:func (s *WebServer) sectionThreadTreePage(c *gin.Context) {
./internal/web/web_threadTreePage.go:16://   - func (s *WebServer) threadTreePage(c *gin.Context) (line ~1596)
./internal/web/web_threadTreePage.go:18://   - func (s *WebServer) sectionThreadTreePage(c *gin.Context) (line ~1670)
./internal/web/web_threadTreePage.go:20://   - func (s *WebServer) threadTreeDemoPage(c *gin.Context) (line ~1526)
./internal/web/web_threadTreePage.go:22://   - func (s *WebServer) handleThreadTreeAPI(c *gin.Context) (line ~1532)
./internal/web/web_threadTreePage.go:254:func (s *WebServer) threadTreeDemoPage(c *gin.Context) {
./internal/web/web_threadTreePage.go:28:func (s *WebServer) handleThreadTreeAPI(c *gin.Context) {
./internal/web/web_threadTreePage.go:92:func (s *WebServer) threadTreePage(c *gin.Context) {
./internal/web/web_utils.go:117:func (s *WebServer) isAdminUser(user *models.User) bool {
./internal/web/web_utils.go:134:func (s *WebServer) renderError(c *gin.Context, statusCode int, message string, errstring string) {
./internal/web/web_utils.go:158:func (s *WebServer) renderTemplate(c *gin.Context, templateName string, data interface{}) {
./internal/web/web_utils.go:170:func (s *WebServer) GetGroupCount() int {
./internal/web/web_utils.go:19:// - func (s *WebServer) GetPort() int (line ~235)
./internal/web/web_utils.go:21:// - func (s *WebServer) NNTPGetTCPPort() int (line ~239)
./internal/web/web_utils.go:23:// - func (s *WebServer) NNTPGetTLSPort() int (line ~247)
./internal/web/web_utils.go:25:// - func (s *WebServer) getBaseTemplateData(c *gin.Context, title string) TemplateData (line ~255)
./internal/web/web_utils.go:27:// - func (s *WebServer) isAdminUser(user *models.User) bool (line ~279)
./internal/web/web_utils.go:29:// - func (s *WebServer) renderError(c *gin.Context, statusCode int, message string, errstring string) (line ~1280)
./internal/web/web_utils.go:31:// - func (s *WebServer) renderTemplate(c *gin.Context, templateName string, data interface{}) (line ~1308)
./internal/web/web_utils.go:33:// - func (s *WebServer) GetGroupCount() int (line ~1320)
./internal/web/web_utils.go:44:func (s *WebServer) GetPort() int {
./internal/web/web_utils.go:49:func (s *WebServer) NNTPGetTCPPort() int {
./internal/web/web_utils.go:57:func (s *WebServer) NNTPGetTLSPort() int {
./internal/web/web_utils.go:65:func (s *WebServer) getBaseTemplateData(c *gin.Context, title string) TemplateData {
