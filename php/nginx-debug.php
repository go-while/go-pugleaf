<?php
/**
 * Debug script to see what Nginx is passing to PHP
 * Place this as /var/www/nginx/pugleaf/nginx-debug.php
 */

header('Content-Type: text/html; charset=utf-8');

echo "<h1>Nginx → PHP Debug Information</h1>";

echo "<h2>REQUEST INFORMATION</h2>";
echo "<table border='1' cellpadding='5' cellspacing='0'>";
echo "<tr><th>Variable</th><th>Value</th></tr>";

$request_vars = [
    'REQUEST_METHOD',
    'REQUEST_URI', 
    'QUERY_STRING',
    'PATH_INFO',
    'PATH_TRANSLATED',
    'SCRIPT_NAME',
    'SCRIPT_FILENAME',
    'PHP_SELF',
    'HTTP_HOST',
    'SERVER_NAME',
    'DOCUMENT_ROOT'
];

foreach ($request_vars as $var) {
    $value = $_SERVER[$var] ?? 'NOT SET';
    echo "<tr><td><strong>$var</strong></td><td>" . htmlspecialchars($value) . "</td></tr>";
}

echo "</table>";

echo "<h2>PARSED URL COMPONENTS</h2>";
$request_uri = $_SERVER['REQUEST_URI'] ?? '';
if ($request_uri) {
    $parsed = parse_url($request_uri);
    echo "<table border='1' cellpadding='5' cellspacing='0'>";
    foreach ($parsed as $key => $value) {
        echo "<tr><td><strong>$key</strong></td><td>" . htmlspecialchars($value) . "</td></tr>";
    }
    echo "</table>";
}

echo "<h2>FULL \$_SERVER ARRAY</h2>";
echo "<pre>";
print_r($_SERVER);
echo "</pre>";

echo "<h2>ROUTING TEST</h2>";
echo "<p><strong>Current URL:</strong> " . htmlspecialchars($request_uri) . "</p>";

// Simple routing test
$path = parse_url($request_uri, PHP_URL_PATH);
$path = trim($path, '/');

echo "<p><strong>Cleaned Path:</strong> '" . htmlspecialchars($path) . "'</p>";

if (empty($path)) {
    echo "<p>→ This should route to: <strong>HomeController@index</strong></p>";
} elseif ($path === 'groups') {
    echo "<p>→ This should route to: <strong>GroupsController@index</strong></p>";
} elseif (preg_match('#^groups/([^/]+)$#', $path, $matches)) {
    echo "<p>→ This should route to: <strong>GroupsController@show</strong> with group: " . htmlspecialchars($matches[1]) . "</p>";
} else {
    echo "<p>→ This should route to: <strong>Unknown/Custom route</strong></p>";
}

echo "<h2>TEST LINKS</h2>";
echo "<ul>";
echo "<li><a href='/'>Home</a></li>";
echo "<li><a href='/groups'>Groups</a></li>";
echo "<li><a href='/groups/test.group'>Test Group</a></li>";
echo "<li><a href='/search'>Search</a></li>";
echo "<li><a href='/stats'>Stats</a></li>";
echo "<li><a href='/help'>Help</a></li>";
echo "</ul>";

echo "<hr>";
echo "<p><small>Debug info generated at " . date('Y-m-d H:i:s') . "</small></p>";
?>
