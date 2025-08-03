<?php
/**
 * API Client for go-pugleaf Backend
 * Enhanced version with proper error handling and caching
 */

class PugleafApiClient {
    private $baseUrl;
    private $timeout;
    private $cache = [];
    private $cacheTimeout = 300; // 5 minutes

    public function __construct($baseUrl) {
        $this->baseUrl = rtrim($baseUrl, '/');
        $this->timeout = 30;
    }

    /**
     * Make HTTP request with caching
     */
    private function request($endpoint, $method = 'GET', $data = null, $useCache = true) {
        $cacheKey = md5($method . ':' . $endpoint . ':' . serialize($data));

        // Check cache
        if ($useCache && isset($this->cache[$cacheKey])) {
            $cached = $this->cache[$cacheKey];
            if (time() - $cached['timestamp'] < $this->cacheTimeout) {
                return $cached['data'];
            }
        }

        $url = $this->baseUrl . $endpoint;

        $ch = curl_init();
        curl_setopt_array($ch, [
            CURLOPT_URL => $url,
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_TIMEOUT => $this->timeout,
            CURLOPT_CUSTOMREQUEST => $method,
            CURLOPT_HTTPHEADER => [
                'Content-Type: application/json',
                'Accept: application/json',
                'User-Agent: PugleafPHP/1.0'
            ],
            CURLOPT_FOLLOWLOCATION => true,
            CURLOPT_MAXREDIRS => 3,
            // SSL Options
            CURLOPT_SSL_VERIFYPEER => true,
            CURLOPT_SSL_VERIFYHOST => 2,
            // If you have SSL certificate issues, uncomment the next line (NOT recommended for production)
            // CURLOPT_SSL_VERIFYPEER => false,
        ]);

        if ($data && in_array($method, ['POST', 'PUT', 'PATCH'])) {
            curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($data));
        }

        $response = curl_exec($ch);
        $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
        $curlError = curl_error($ch);
        curl_close($ch);

        if ($curlError) {
            throw new Exception("Connection error: " . $curlError);
        }

        if ($httpCode >= 400) {
            $errorMsg = "HTTP $httpCode";
            if ($response) {
                $decoded = json_decode($response, true);
                if (isset($decoded['error'])) {
                    $errorMsg .= ": " . $decoded['error'];
                }
            }
            throw new Exception($errorMsg);
        }

        $result = json_decode($response, true);
        if (json_last_error() !== JSON_ERROR_NONE) {
            throw new Exception("Invalid JSON response: " . json_last_error_msg());
        }

        // Cache successful responses
        if ($useCache && $httpCode == 200) {
            $this->cache[$cacheKey] = [
                'data' => $result,
                'timestamp' => time()
            ];
        }

        return $result;
    }

    /**
     * Get sections (not implemented in Go backend yet)
     */
    public function getSections() {
        // Go backend doesn't have sections API yet, return empty for now
        return ['data' => [], 'page' => 1, 'page_size' => 20, 'total_count' => 0, 'total_pages' => 1];
    }

    /**
     * Get newsgroups - matches Go backend /api/v1/groups endpoint
     */
    public function getNewsgroups($page = 1, $pageSize = 20) {
        return $this->request("/groups?page={$page}&page_size={$pageSize}");
    }

    /**
     * Get section groups (not implemented in Go backend yet)
     */
    public function getSectionGroups($section, $page = 1, $pageSize = 20) {
        // Go backend doesn't have sections API yet, return empty for now
        return ['data' => [], 'page' => 1, 'page_size' => 20, 'total_count' => 0, 'total_pages' => 1];
    }

    /**
     * Get group overview
     */
    public function getGroupOverview($group, $page = 1, $pageSize = 20) {
        $group = urlencode($group);
        return $this->request("/groups/{$group}/overview?page={$page}&page_size={$pageSize}");
    }

    /**
     * Get article by number
     */
    public function getArticle($group, $articleNum) {
        $group = urlencode($group);
        return $this->request("/groups/{$group}/articles/{$articleNum}");
    }

    /**
     * Get article by message ID - matches Go backend endpoint
     */
    public function getArticleByMessageId($group, $messageId) {
        $group = urlencode($group);
        $messageId = urlencode($messageId);
        return $this->request("/groups/{$group}/message/{$messageId}");
    }

    /**
     * Get group threads - Go backend returns threads directly (not paginated)
     */
    public function getGroupThreads($group, $page = 1, $pageSize = 20) {
        $group = urlencode($group);
        return $this->request("/groups/{$group}/threads");
    }

    /**
     * Search (not implemented in Go backend yet)
     */
    public function search($query, $group = null, $page = 1, $pageSize = 20) {
        // Go backend doesn't have search API yet, return empty for now
        return ['data' => [], 'page' => 1, 'page_size' => 20, 'total_count' => 0, 'total_pages' => 1];
    }

    /**
     * Get stats - now fetches real data from Go backend
     */
    public function getStats() {
        try {
            return $this->request('/stats');
        } catch (Exception $e) {
            // Fallback to basic info if API is unavailable
            error_log("Failed to fetch stats from backend: " . $e->getMessage());
            return [
                'total_groups' => 0,
                'total_articles' => 0,
                'total_threads' => 0,
                'active_groups' => 0,
                'total_size' => 0,
                'top_groups' => [],
                'last_update' => date('c'),
                'backend_version' => 'Unknown',
                'uptime' => 'Unknown'
            ];
        }
    }

    /**
     * Health check
     */
    public function healthCheck() {
        try {
            $this->request('/health');
            return true;
        } catch (Exception $e) {
            return false;
        }
    }

    /**
     * Clear cache
     */
    public function clearCache() {
        $this->cache = [];
    }
}
