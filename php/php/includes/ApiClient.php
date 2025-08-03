<?php
/**
 * API Client for go-pugleaf Backend
 * Handles all communication with the Go backend
 */

class ApiClient {
    private $baseUrl;
    private $timeout;

    public function __construct() {
        $this->baseUrl = API_BASE_URL;
        $this->timeout = API_TIMEOUT;
    }

    /**
     * Make HTTP request to backend API
     */
    private function makeRequest($endpoint, $method = 'GET', $data = null) {
        $url = $this->baseUrl . $endpoint;

        $ch = curl_init();
        curl_setopt_array($ch, [
            CURLOPT_URL => $url,
            CURLOPT_RETURNTRANSFER => true,
            CURLOPT_TIMEOUT => $this->timeout,
            CURLOPT_CUSTOMREQUEST => $method,
            CURLOPT_HTTPHEADER => [
                'Content-Type: application/json',
                'Accept: application/json'
            ],
            CURLOPT_SSL_VERIFYPEER => false, // For development
            CURLOPT_FOLLOWLOCATION => true
        ]);

        if ($data && in_array($method, ['POST', 'PUT', 'PATCH'])) {
            curl_setopt($ch, CURLOPT_POSTFIELDS, json_encode($data));
        }

        $response = curl_exec($ch);
        $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
        $error = curl_error($ch);
        curl_close($ch);

        if ($error) {
            throw new Exception("cURL Error: " . $error);
        }

        if ($httpCode >= 400) {
            throw new Exception("HTTP Error: " . $httpCode);
        }

        return json_decode($response, true);
    }

    /**
     * Get all sections
     */
    public function getSections() {
        return $this->makeRequest('/sections');
    }

    /**
     * Get newsgroups (paginated)
     */
    public function getNewsgroups($page = 1, $pageSize = 20) {
        return $this->makeRequest("/newsgroups?page={$page}&page_size={$pageSize}");
    }

    /**
     * Get newsgroups for a specific section
     */
    public function getSectionGroups($sectionName, $page = 1, $pageSize = 20) {
        $section = urlencode($sectionName);
        return $this->makeRequest("/sections/{$section}/groups?page={$page}&page_size={$pageSize}");
    }

    /**
     * Get group overview (articles list)
     */
    public function getGroupOverview($groupName, $page = 1, $pageSize = 20) {
        $group = urlencode($groupName);
        return $this->makeRequest("/groups/{$group}/overview?page={$page}&page_size={$pageSize}");
    }

    /**
     * Get article by number
     */
    public function getArticle($groupName, $articleNum) {
        $group = urlencode($groupName);
        return $this->makeRequest("/groups/{$group}/articles/{$articleNum}");
    }

    /**
     * Get article by message ID
     */
    public function getArticleByMessageId($messageId) {
        $msgId = urlencode($messageId);
        return $this->makeRequest("/articles/{$msgId}");
    }

    /**
     * Get thread for a group
     */
    public function getGroupThreads($groupName, $page = 1, $pageSize = 20) {
        $group = urlencode($groupName);
        return $this->makeRequest("/groups/{$group}/threads?page={$page}&page_size={$pageSize}");
    }

    /**
     * Search articles
     */
    public function searchArticles($query, $group = null, $page = 1, $pageSize = 20) {
        $params = [
            'q' => $query,
            'page' => $page,
            'page_size' => $pageSize
        ];

        if ($group) {
            $params['group'] = $group;
        }

        $queryString = http_build_query($params);
        return $this->makeRequest("/search?{$queryString}");
    }

    /**
     * Get system stats
     */
    public function getStats() {
        return $this->makeRequest('/stats');
    }

    /**
     * Health check
     */
    public function healthCheck() {
        try {
            $this->makeRequest('/health');
            return true;
        } catch (Exception $e) {
            return false;
        }
    }
}
