<?php
/**
 * Debug Router Test - Test the routing system
 */

// Include the configuration
if (file_exists(__DIR__ . '/config.inc.php')) {
    require_once __DIR__ . '/config.inc.php';
} else {
    die('Error: config.inc.php not found.');
}

// Include dependencies
require_once __DIR__ . '/includes/router.php';
require_once __DIR__ . '/includes/api_client.php';
require_once __DIR__ . '/includes/template_engine.php';
require_once __DIR__ . '/includes/utils.php';

header('Content-Type: text/html; charset=utf-8');

echo "<h1>Router Debug Test</h1>";

try {
    echo "<h2>Step 1: Create components</h2>";
    $api = new PugleafApiClient(PUGLEAF_API_BASE);
    $template = new TemplateEngine(__DIR__ . '/templates');
    $router = new SimpleRouter();
    echo "<p>✓ All components created successfully</p>";

    echo "<h2>Step 2: Add routes</h2>";
    $router->add('GET', '/groups', 'GroupsController@index');
    echo "<p>✓ Route added: GET /groups -> GroupsController@index</p>";

    echo "<h2>Step 3: Simulate request</h2>";
    // Simulate the request that would normally come from Apache/Nginx
    $_SERVER['REQUEST_METHOD'] = 'GET';
    $_SERVER['REQUEST_URI'] = '/groups';
    $_GET = [];
    echo "<p>✓ Request simulation set up</p>";

    echo "<h2>Step 4: Dispatch route</h2>";
    ob_start();
    $router->dispatch();
    $output = ob_get_clean();

    echo "<p>✓ Route dispatched successfully</p>";
    echo "<p><strong>Output length:</strong> " . strlen($output) . " bytes</p>";

    if (strlen($output) > 0) {
        echo "<p><strong>Output preview (first 500 chars):</strong></p>";
        echo "<pre>" . htmlspecialchars(substr($output, 0, 500)) . "</pre>";
    }

} catch (Exception $e) {
    echo "<p><strong>ERROR:</strong> " . htmlspecialchars($e->getMessage()) . "</p>";
    echo "<p><strong>Error in file:</strong> " . htmlspecialchars($e->getFile()) . ":" . $e->getLine() . "</p>";
    echo "<p><strong>Stack trace:</strong></p>";
    echo "<pre>" . htmlspecialchars($e->getTraceAsString()) . "</pre>";
}

echo "<h2>Debug Info</h2>";
echo "<p><strong>Request Method:</strong> " . ($_SERVER['REQUEST_METHOD'] ?? 'undefined') . "</p>";
echo "<p><strong>Request URI:</strong> " . ($_SERVER['REQUEST_URI'] ?? 'undefined') . "</p>";
echo "<p><strong>Script Name:</strong> " . ($_SERVER['SCRIPT_NAME'] ?? 'undefined') . "</p>";

?>
