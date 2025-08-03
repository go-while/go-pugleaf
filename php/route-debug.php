<?php
/**
 * Route Debug Test
 */

// Include the configuration
if (file_exists(__DIR__ . '/config.inc.php')) {
    require_once __DIR__ . '/config.inc.php';
} else {
    die('Error: config.inc.php not found.');
}

// Include dependencies
require_once __DIR__ . '/includes/router.php';

header('Content-Type: text/html; charset=utf-8');

echo "<h1>Route Debug Test</h1>";

$router = new SimpleRouter();
$router->add('GET', '/groups/{group}/threads', 'GroupsController@threads');

$testPaths = [
    '/groups/0.verizon.discussion-general/threads',
    '/groups/comp.programming/threads',
    '/groups/test/threads'
];

foreach ($testPaths as $path) {
    echo "<h2>Testing path: " . htmlspecialchars($path) . "</h2>";

    // Test the route matching manually
    $pattern = '/groups/{group}/threads';
    $params = [];

    // Convert pattern to regex (same logic as router)
    $regex = preg_quote($pattern, '#');
    $regex = preg_replace('/\\\{([^}]+)\\\}/', '([^/]+)', $regex);
    $regex = '#^' . $regex . '$#';

    echo "<p><strong>Generated regex:</strong> " . htmlspecialchars($regex) . "</p>";

    if (preg_match($regex, $path, $matches)) {
        array_shift($matches); // Remove full match
        echo "<p><strong>✅ Match found!</strong></p>";
        echo "<p><strong>Captured groups:</strong> " . implode(', ', $matches) . "</p>";

        // Extract parameter names
        preg_match_all('/\{([^}]+)\}/', $pattern, $paramNames);
        if (isset($paramNames[1])) {
            foreach ($paramNames[1] as $index => $name) {
                if (isset($matches[$index])) {
                    $params[$name] = $matches[$index];
                    echo "<p><strong>\$params['{$name}']:</strong> " . htmlspecialchars($matches[$index]) . "</p>";
                }
            }
        }
    } else {
        echo "<p><strong>❌ No match</strong></p>";
    }

    echo "<hr>";
}

?>
