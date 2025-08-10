<?php
/**
 * Utility functions for the PHP frontend
 */

/**
 * Escape HTML output
 */
function h($string) {
    return htmlspecialchars($string ?? '', ENT_QUOTES, 'UTF-8');
}

/**
 * Generate CSRF token input
 */
function csrf_token_input() {
    return '<input type="hidden" name="' . h(CSRF_TOKEN_NAME) . '" value="' . h($_SESSION[CSRF_TOKEN_NAME]) . '">';
}

/**
 * Verify CSRF token
 */
function verify_csrf_token($token) {
    return isset($_SESSION[CSRF_TOKEN_NAME]) && hash_equals($_SESSION[CSRF_TOKEN_NAME], $token);
}

/**
 * Format date/time for display
 */
function format_datetime($datetime) {
    if (!$datetime) return '';

    $timestamp = is_numeric($datetime) ? $datetime : strtotime($datetime);
    if (!$timestamp) return $datetime;

    return date('Y-m-d H:i:s', $timestamp);
}

/**
 * Format relative time (e.g., "2 hours ago")
 */
function format_relative_time($datetime) {
    if (!$datetime) return '';

    $timestamp = is_numeric($datetime) ? $datetime : strtotime($datetime);
    if (!$timestamp) return $datetime;

    $diff = time() - $timestamp;

    if ($diff < 60) return $diff . ' seconds ago';
    if ($diff < 3600) return floor($diff / 60) . ' minutes ago';
    if ($diff < 86400) return floor($diff / 3600) . ' hours ago';
    if ($diff < 2592000) return floor($diff / 86400) . ' days ago';
    if ($diff < 31536000) return floor($diff / 2592000) . ' months ago';

    return floor($diff / 31536000) . ' years ago';
}

/**
 * Format file size
 */
function format_bytes($size) {
    if ($size == 0) return '0 B';

    $units = ['B', 'KB', 'MB', 'GB', 'TB'];
    $power = floor(log($size, 1024));

    return round($size / pow(1024, $power), 2) . ' ' . $units[$power];
}

/**
 * Truncate text
 */
function truncate($text, $length = 100, $suffix = '...') {
    if (mb_strlen($text) <= $length) return $text;

    return mb_substr($text, 0, $length) . $suffix;
}

/**
 * Generate pagination links
 */
function generate_pagination($currentPage, $totalPages, $baseUrl, $params = []) {
    if ($totalPages <= 1) return '';

    $html = '<nav aria-label="Pagination"><ul class="pagination justify-content-center">';

    // Previous page
    if ($currentPage > 1) {
        $prevParams = array_merge($params, ['page' => $currentPage - 1]);
        $prevUrl = $baseUrl . '?' . http_build_query($prevParams);
        $html .= '<li class="page-item"><a class="page-link" href="' . h($prevUrl) . '">Previous</a></li>';
    } else {
        $html .= '<li class="page-item disabled"><span class="page-link">Previous</span></li>';
    }

    // Page numbers
    $start = max(1, $currentPage - 2);
    $end = min($totalPages, $currentPage + 2);

    if ($start > 1) {
        $firstParams = array_merge($params, ['page' => 1]);
        $firstUrl = $baseUrl . '?' . http_build_query($firstParams);
        $html .= '<li class="page-item"><a class="page-link" href="' . h($firstUrl) . '">1</a></li>';

        if ($start > 2) {
            $html .= '<li class="page-item disabled"><span class="page-link">...</span></li>';
        }
    }

    for ($i = $start; $i <= $end; $i++) {
        if ($i == $currentPage) {
            $html .= '<li class="page-item active"><span class="page-link">' . $i . '</span></li>';
        } else {
            $pageParams = array_merge($params, ['page' => $i]);
            $pageUrl = $baseUrl . '?' . http_build_query($pageParams);
            $html .= '<li class="page-item"><a class="page-link" href="' . h($pageUrl) . '">' . $i . '</a></li>';
        }
    }

    if ($end < $totalPages) {
        if ($end < $totalPages - 1) {
            $html .= '<li class="page-item disabled"><span class="page-link">...</span></li>';
        }

        $lastParams = array_merge($params, ['page' => $totalPages]);
        $lastUrl = $baseUrl . '?' . http_build_query($lastParams);
        $html .= '<li class="page-item"><a class="page-link" href="' . h($lastUrl) . '">' . $totalPages . '</a></li>';
    }

    // Next page
    if ($currentPage < $totalPages) {
        $nextParams = array_merge($params, ['page' => $currentPage + 1]);
        $nextUrl = $baseUrl . '?' . http_build_query($nextParams);
        $html .= '<li class="page-item"><a class="page-link" href="' . h($nextUrl) . '">Next</a></li>';
    } else {
        $html .= '<li class="page-item disabled"><span class="page-link">Next</span></li>';
    }

    $html .= '</ul></nav>';

    return $html;
}

/**
 * Sanitize article body for display
 */
function sanitize_article_body($body) {
    // Basic HTML sanitization - expand as needed
    $body = h($body);

    // Convert URLs to links
    $body = preg_replace(
        '/(https?:\/\/[^\s<>"\']+)/i',
        '<a href="$1" target="_blank" rel="noopener">$1</a>',
        $body
    );

    // Convert line breaks
    $body = nl2br($body);

    return $body;
}

/**
 * Extract name from email address for display
 */
function extract_display_name($from) {
    if (preg_match('/^(.+?)\s*<.+>$/', $from, $matches)) {
        return trim($matches[1], '"');
    }

    if (preg_match('/^(.+)@.+$/', $from, $matches)) {
        return $matches[1];
    }

    return $from;
}

/**
 * Build breadcrumb navigation
 */
function build_breadcrumb($items) {
    if (empty($items)) return '';

    $html = '<nav aria-label="breadcrumb"><ol class="breadcrumb">';

    $lastIndex = count($items) - 1;
    foreach ($items as $index => $item) {
        if ($index === $lastIndex) {
            $html .= '<li class="breadcrumb-item active" aria-current="page">' . h($item['title']) . '</li>';
        } else {
            $html .= '<li class="breadcrumb-item"><a href="' . h($item['url']) . '">' . h($item['title']) . '</a></li>';
        }
    }

    $html .= '</ol></nav>';

    return $html;
}
