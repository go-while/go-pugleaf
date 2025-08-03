<?php
/**
 * Simple Template Engine
 * Converts Go templates to PHP templates
 */

class TemplateEngine {
    private $templateDir;
    private $data = [];

    public function __construct($templateDir) {
        $this->templateDir = rtrim($templateDir, '/');
    }

    /**
     * Render template
     */
    public function render($template, $data = []) {
        $this->data = array_merge($this->data, $data);

        // Extract data to variables
        extract($this->data);

        $templateFile = $this->templateDir . '/' . $template . '.php';

        if (!file_exists($templateFile)) {
            throw new Exception("Template not found: {$templateFile}");
        }

        // Start output buffering
        ob_start();

        try {
            include $templateFile;
            $content = ob_get_contents();
        } catch (Exception $e) {
            ob_end_clean();
            throw $e;
        }

        ob_end_clean();
        echo $content;
    }

    /**
     * Set global data
     */
    public function setData($key, $value) {
        $this->data[$key] = $value;
    }

    /**
     * Get template data
     */
    public function getData($key = null) {
        if ($key === null) {
            return $this->data;
        }

        return isset($this->data[$key]) ? $this->data[$key] : null;
    }
}

/**
 * Template helper functions
 */

function h($string) {
    return htmlspecialchars($string ?? '', ENT_QUOTES, 'UTF-8');
}

function formatDate($timestamp) {
    if (!$timestamp) return '';

    if (is_string($timestamp)) {
        $timestamp = strtotime($timestamp);
    }

    return date('Y-m-d H:i:s', $timestamp);
}

function formatRelativeTime($timestamp) {
    if (!$timestamp) return '';

    if (is_string($timestamp)) {
        $timestamp = strtotime($timestamp);
    }

    $diff = time() - $timestamp;

    if ($diff < 60) return $diff . ' seconds ago';
    if ($diff < 3600) return floor($diff / 60) . ' minutes ago';
    if ($diff < 86400) return floor($diff / 3600) . ' hours ago';
    if ($diff < 2592000) return floor($diff / 86400) . ' days ago';

    return date('Y-m-d', $timestamp);
}

function truncate($text, $length = 100) {
    if (mb_strlen($text) <= $length) return $text;
    return mb_substr($text, 0, $length) . '...';
}

function sanitizeArticleBody($body) {
    // Basic sanitization
    $body = h($body);

    // Convert URLs to links
    $body = preg_replace(
        '/(https?:\/\/[^\s<>"\']+)/i',
        '<a href="$1" target="_blank" rel="noopener noreferrer">$1</a>',
        $body
    );

    // Convert line breaks
    $body = nl2br($body);

    return $body;
}

function pagination($currentPage, $totalPages, $baseUrl, $params = []) {
    if ($totalPages <= 1) return '';

    $html = '<nav><ul class="pagination justify-content-center">';

    // Previous
    if ($currentPage > 1) {
        $prevParams = array_merge($params, ['page' => $currentPage - 1]);
        $prevUrl = $baseUrl . '?' . http_build_query($prevParams);
        $html .= '<li class="page-item"><a class="page-link" href="' . h($prevUrl) . '">Previous</a></li>';
    }

    // Pages
    $start = max(1, $currentPage - 2);
    $end = min($totalPages, $currentPage + 2);

    for ($i = $start; $i <= $end; $i++) {
        if ($i == $currentPage) {
            $html .= '<li class="page-item active"><span class="page-link">' . $i . '</span></li>';
        } else {
            $pageParams = array_merge($params, ['page' => $i]);
            $pageUrl = $baseUrl . '?' . http_build_query($pageParams);
            $html .= '<li class="page-item"><a class="page-link" href="' . h($pageUrl) . '">' . $i . '</a></li>';
        }
    }

    // Next
    if ($currentPage < $totalPages) {
        $nextParams = array_merge($params, ['page' => $currentPage + 1]);
        $nextUrl = $baseUrl . '?' . http_build_query($nextParams);
        $html .= '<li class="page-item"><a class="page-link" href="' . h($nextUrl) . '">Next</a></li>';
    }

    $html .= '</ul></nav>';

    return $html;
}
