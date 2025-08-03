<?php
/**
 * go-pugleaf PHP Frontend Configuration
 *
 * Copy this file to config.inc.php and customize the settings for your environment
 */

// API Configuration - Update these to match your go-pugleaf backend
define('PUGLEAF_API_BASE', 'http://localhost:8080/api/v1');
define('PUGLEAF_WEB_BASE', 'http://localhost:8080');

// Debug Mode - Set to false in production
define('DEBUG_MODE', true);

// Cache Configuration
define('CACHE_TTL', 300); // 5 minutes default cache TTL

// Template Configuration
define('TEMPLATE_CACHE', false); // Enable template caching in production

// Site Configuration
define('SITE_NAME', 'Pugleaf NNTP Reader');
define('SITE_DESCRIPTION', 'A modern NNTP newsgroup reader powered by go-pugleaf');

// Pagination
define('ARTICLES_PER_PAGE', 25);
define('GROUPS_PER_PAGE', 50);

// Security
define('SESSION_TIMEOUT', 3600); // 1 hour
define('CSRF_TOKEN_EXPIRE', 1800); // 30 minutes

// Performance
define('MAX_SEARCH_RESULTS', 100);
define('API_TIMEOUT', 30); // seconds

// Optional: Custom paths (usually not needed)
// define('CUSTOM_TEMPLATE_PATH', '/path/to/custom/templates');
// define('CUSTOM_CACHE_PATH', '/path/to/cache');
