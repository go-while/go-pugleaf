<?php
/**
 * Simple Router for PHP Frontend
 * Clean URL routing without legacy baggage
 */

class Router {
    private $routes = [];
    private $apiClient;

    public function __construct(ApiClient $apiClient) {
        $this->apiClient = $apiClient;
    }

    /**
     * Add route
     */
    public function addRoute($pattern, $handler) {
        $this->routes[$pattern] = $handler;
    }

    /**
     * Route the request
     */
    public function route() {
        $path = parse_url($_SERVER['REQUEST_URI'], PHP_URL_PATH);
        $path = rtrim($path, '/') ?: '/';

        foreach ($this->routes as $pattern => $handler) {
            if ($this->matchRoute($pattern, $path, $matches)) {
                return $this->executeHandler($handler, $matches);
            }
        }

        // 404 Not Found
        $this->render404();
    }

    /**
     * Match route pattern
     */
    private function matchRoute($pattern, $path, &$matches) {
        // Convert route pattern to regex
        $regex = preg_replace('/\{([^}]+)\}/', '([^/]+)', $pattern);
        $regex = '#^' . $regex . '$#';

        return preg_match($regex, $path, $matches);
    }

    /**
     * Execute route handler
     */
    private function executeHandler($handler, $matches) {
        array_shift($matches); // Remove full match

        if (is_callable($handler)) {
            return call_user_func_array($handler, [$matches, $this->apiClient]);
        }

        if (is_string($handler) && method_exists($this, $handler)) {
            return $this->$handler($matches);
        }

        throw new Exception("Invalid route handler");
    }

    /**
     * Render 404 page
     */
    private function render404() {
        http_response_code(404);
        include 'templates/404.php';
        exit;
    }

    /**
     * Setup default routes
     */
    public function setupRoutes() {
        // Home page - sections list
        $this->addRoute('/', [$this, 'homePage']);

        // Groups listing
        $this->addRoute('/groups', [$this, 'groupsPage']);

        // Section page
        $this->addRoute('/sections/{section}', [$this, 'sectionPage']);

        // Group overview
        $this->addRoute('/groups/{group}', [$this, 'groupPage']);

        // Article view
        $this->addRoute('/groups/{group}/articles/{num}', [$this, 'articlePage']);

        // Article by message ID
        $this->addRoute('/articles/{messageId}', [$this, 'articleByMessageIdPage']);

        // Group threads
        $this->addRoute('/groups/{group}/threads', [$this, 'threadsPage']);

        // Search
        $this->addRoute('/search', [$this, 'searchPage']);

        // Stats
        $this->addRoute('/stats', [$this, 'statsPage']);

        // API health check
        $this->addRoute('/health', [$this, 'healthPage']);
    }

    /**
     * Home page - sections list
     */
    public function homePage($matches) {
        try {
            $sections = $this->apiClient->getSections();
            $stats = $this->apiClient->getStats();

            include 'templates/home.php';
        } catch (Exception $e) {
            $this->renderError("Failed to load home page", $e->getMessage());
        }
    }

    /**
     * Groups listing page
     */
    public function groupsPage($matches) {
        $page = (int)($_GET['page'] ?? 1);
        $pageSize = min((int)($_GET['page_size'] ?? 20), MAX_PAGE_SIZE);

        try {
            $result = $this->apiClient->getNewsgroups($page, $pageSize);

            include 'templates/groups.php';
        } catch (Exception $e) {
            $this->renderError("Failed to load groups", $e->getMessage());
        }
    }

    /**
     * Section page
     */
    public function sectionPage($matches) {
        $sectionName = $matches[0];
        $page = (int)($_GET['page'] ?? 1);
        $pageSize = min((int)($_GET['page_size'] ?? 20), MAX_PAGE_SIZE);

        try {
            $result = $this->apiClient->getSectionGroups($sectionName, $page, $pageSize);

            include 'templates/section.php';
        } catch (Exception $e) {
            $this->renderError("Failed to load section", $e->getMessage());
        }
    }

    /**
     * Group page - articles overview
     */
    public function groupPage($matches) {
        $groupName = $matches[0];
        $page = (int)($_GET['page'] ?? 1);
        $pageSize = min((int)($_GET['page_size'] ?? 20), MAX_PAGE_SIZE);

        try {
            $result = $this->apiClient->getGroupOverview($groupName, $page, $pageSize);

            include 'templates/group.php';
        } catch (Exception $e) {
            $this->renderError("Failed to load group", $e->getMessage());
        }
    }

    /**
     * Article page
     */
    public function articlePage($matches) {
        $groupName = $matches[0];
        $articleNum = (int)$matches[1];

        try {
            $article = $this->apiClient->getArticle($groupName, $articleNum);

            include 'templates/article.php';
        } catch (Exception $e) {
            $this->renderError("Failed to load article", $e->getMessage());
        }
    }

    /**
     * Article by message ID page
     */
    public function articleByMessageIdPage($matches) {
        $messageId = $matches[0];

        try {
            $article = $this->apiClient->getArticleByMessageId($messageId);

            include 'templates/article.php';
        } catch (Exception $e) {
            $this->renderError("Failed to load article", $e->getMessage());
        }
    }

    /**
     * Threads page
     */
    public function threadsPage($matches) {
        $groupName = $matches[0];
        $page = (int)($_GET['page'] ?? 1);
        $pageSize = min((int)($_GET['page_size'] ?? 20), MAX_PAGE_SIZE);

        try {
            $result = $this->apiClient->getGroupThreads($groupName, $page, $pageSize);

            include 'templates/threads.php';
        } catch (Exception $e) {
            $this->renderError("Failed to load threads", $e->getMessage());
        }
    }

    /**
     * Search page
     */
    public function searchPage($matches) {
        $query = $_GET['q'] ?? '';
        $group = $_GET['group'] ?? null;
        $page = (int)($_GET['page'] ?? 1);
        $pageSize = min((int)($_GET['page_size'] ?? 20), MAX_PAGE_SIZE);

        $result = null;
        if ($query) {
            try {
                $result = $this->apiClient->searchArticles($query, $group, $page, $pageSize);
            } catch (Exception $e) {
                $error = $e->getMessage();
            }
        }

        include 'templates/search.php';
    }

    /**
     * Stats page
     */
    public function statsPage($matches) {
        try {
            $stats = $this->apiClient->getStats();

            include 'templates/stats.php';
        } catch (Exception $e) {
            $this->renderError("Failed to load stats", $e->getMessage());
        }
    }

    /**
     * Health check page
     */
    public function healthPage($matches) {
        $healthy = $this->apiClient->healthCheck();

        header('Content-Type: application/json');
        echo json_encode(['status' => $healthy ? 'healthy' : 'unhealthy']);
        exit;
    }

    /**
     * Render error page
     */
    private function renderError($title, $message) {
        http_response_code(500);
        include 'templates/error.php';
        exit;
    }
}
