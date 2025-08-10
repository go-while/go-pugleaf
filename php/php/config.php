<?php
/**
 * go-pugleaf PHP Frontend Configuration
 * Clean PHP frontend for go-pugleaf backend
 */

// Backend API Configuration
define('API_BASE_URL', 'http://localhost:8080/api/v1');
define('API_TIMEOUT', 30);

// Database Configuration (for direct SQLite access if needed)
define('DB_DATA_DIR', '../data');
define('DB_MAIN_FILE', DB_DATA_DIR . '/main.db');

// Security Configuration
define('SESSION_TIMEOUT', 3600); // 1 hour
define('CSRF_TOKEN_NAME', 'csrf_token');
define('MAX_PAGE_SIZE', 100);

// Site Configuration
define('SITE_NAME', 'PugLeaf');
define('SITE_DESCRIPTION', 'Modern NNTP Reader');
define('THEME', 'default');

// Error Reporting
ini_set('display_errors', 0);
ini_set('log_errors', 1);
error_reporting(E_ALL);

// Start secure session
session_start([
    'cookie_httponly' => true,
    'cookie_secure' => isset($_SERVER['HTTPS']),
    'cookie_samesite' => 'Strict',
    'use_strict_mode' => true,
    'use_only_cookies' => true
]);

// Generate CSRF token if not exists
if (!isset($_SESSION[CSRF_TOKEN_NAME])) {
    $_SESSION[CSRF_TOKEN_NAME] = bin2hex(random_bytes(32));
}
