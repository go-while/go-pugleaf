<?php
/**
 * Utility functions for the PHP frontend
 */

/**
 * Format bytes
 */
function formatBytes($bytes) {
    if ($bytes == 0) return '0 B';

    $units = ['B', 'KB', 'MB', 'GB', 'TB'];
    $power = floor(log($bytes, 1024));

    return round($bytes / pow(1024, $power), 2) . ' ' . $units[$power];
}

/**
 * Extract display name from email
 */
function extractDisplayName($from) {
    if (preg_match('/^(.+?)\s*<.+>$/', $from, $matches)) {
        return trim($matches[1], '"');
    }

    if (preg_match('/^(.+)@.+$/', $from, $matches)) {
        return $matches[1];
    }

    return $from;
}

/**
 * Build breadcrumb
 */
function buildBreadcrumb($items) {
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

/**
 * Generate random string
 */
function generateRandomString($length = 32) {
    return bin2hex(random_bytes($length / 2));
}

/**
 * Safe redirect
 */
function safeRedirect($url, $code = 302) {
    // Validate URL
    if (!filter_var($url, FILTER_VALIDATE_URL) && !preg_match('/^\/[^\/]/', $url)) {
        $url = '/';
    }

    header("Location: $url", true, $code);
    exit;
}

/**
 * Get current URL
 */
function getCurrentUrl() {
    $protocol = isset($_SERVER['HTTPS']) && $_SERVER['HTTPS'] === 'on' ? 'https' : 'http';
    $host = $_SERVER['HTTP_HOST'];
    $uri = $_SERVER['REQUEST_URI'];

    return $protocol . '://' . $host . $uri;
}

/**
 * Get base URL
 */
function getBaseUrl() {
    $protocol = isset($_SERVER['HTTPS']) && $_SERVER['HTTPS'] === 'on' ? 'https' : 'http';
    $host = $_SERVER['HTTP_HOST'];
    $path = dirname($_SERVER['SCRIPT_NAME']);

    return $protocol . '://' . $host . rtrim($path, '/');
}

/**
 * Debug function
 */
function dd($var) {
    echo '<pre>';
    var_dump($var);
    echo '</pre>';
    die();
}

/**
 * Log function
 */
function logMessage($message, $level = 'INFO') {
    $timestamp = date('Y-m-d H:i:s');
    $logEntry = "[$timestamp] [$level] $message" . PHP_EOL;

    error_log($logEntry, 3, __DIR__ . '/../logs/app.log');
}

/**
 * Simple cache implementation
 */
class SimpleCache {
    private static $cache = [];
    private static $ttl = 300; // 5 minutes

    public static function get($key) {
        if (!isset(self::$cache[$key])) {
            return null;
        }

        $item = self::$cache[$key];
        if (time() > $item['expires']) {
            unset(self::$cache[$key]);
            return null;
        }

        return $item['data'];
    }

    public static function set($key, $data, $ttl = null) {
        if ($ttl === null) {
            $ttl = self::$ttl;
        }

        self::$cache[$key] = [
            'data' => $data,
            'expires' => time() + $ttl
        ];
    }

    public static function forget($key) {
        unset(self::$cache[$key]);
    }    public static function flush() {
        self::$cache = [];
    }
}
