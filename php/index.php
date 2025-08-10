<?php
/**
 * go-pugleaf PHP Frontend
 *
 * A clean PHP frontend for the go-pugleaf NNTP server
 * This frontend interfaces with the go-pugleaf backend via API calls
 *
 * @author RockSolid Community
 * @version 1.0.0
 */

// Load configuration
if (file_exists(__DIR__ . '/config.inc.php')) {
    require_once __DIR__ . '/config.inc.php';
} else {
    die('Error: config.inc.php not found. Please copy config.sample.php to config.inc.php and configure it.');
}

// Error reporting for development
if (defined('DEBUG_MODE') && DEBUG_MODE) {
    error_reporting(E_ALL);
    ini_set('display_errors', 1);
}

// Start session for any future session management
session_start();

// Simple router
require_once __DIR__ . '/includes/router.php';
require_once __DIR__ . '/includes/api_client.php';
require_once __DIR__ . '/includes/template_engine.php';
require_once __DIR__ . '/includes/utils.php';

// Initialize components
$api = new PugleafApiClient(PUGLEAF_API_BASE);
$template = new TemplateEngine(__DIR__ . '/templates');
$router = new SimpleRouter();

// Routes
$router->add('GET', '/', 'HomeController@index');
$router->add('GET', '/groups', 'GroupsController@index');
$router->add('GET', '/groups/{group}', 'GroupsController@show');
$router->add('GET', '/groups/{group}/articles/{articleNum}', 'ArticlesController@show');
$router->add('GET', '/groups/{group}/message/{messageId}', 'ArticlesController@showByMessageId');
$router->add('GET', '/groups/{group}/threads', 'GroupsController@threads');
$router->add('GET', '/search', 'SearchController@index');
$router->add('GET', '/stats', 'StatsController@index');
$router->add('GET', '/help', 'HelpController@index');

// Section-based routes (dynamic)
$router->add('GET', '/{section}', 'SectionsController@show');
$router->add('GET', '/{section}/{group}', 'SectionsController@showGroup');
$router->add('GET', '/{section}/{group}/articles/{articleNum}', 'SectionsController@showArticle');

// Handle request
try {
    $router->dispatch();
} catch (Exception $e) {
    if (DEBUG_MODE) {
        echo "<pre>Error: " . $e->getMessage() . "\n";
        echo "File: " . $e->getFile() . ":" . $e->getLine() . "\n";
        echo "Stack trace:\n" . $e->getTraceAsString() . "</pre>";
    } else {
        $template->render('error', [
            'title' => 'Error',
            'message' => 'An error occurred while processing your request.',
            'error_code' => 500
        ]);
    }
}
