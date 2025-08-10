<?php
/**
 * Debug URL Variables - See what's being passed to index.php
 */

header('Content-Type: text/html; charset=utf-8');

echo "<h1>URL Debug Test</h1>";

echo "<h2>REQUEST_URI Analysis</h2>";
$requestUri = $_SERVER['REQUEST_URI'] ?? 'undefined';
echo "<p><strong>Raw REQUEST_URI:</strong> " . htmlspecialchars($requestUri) . "</p>";

$parsedPath = parse_url($requestUri, PHP_URL_PATH);
echo "<p><strong>Parsed PATH:</strong> " . htmlspecialchars($parsedPath) . "</p>";

$trimmedPath = rtrim($parsedPath, '/') ?: '/';
echo "<p><strong>Trimmed PATH:</strong> " . htmlspecialchars($trimmedPath) . "</p>";

echo "<h2>All Server Variables</h2>";
foreach ($_SERVER as $key => $value) {
    if (strpos($key, 'REQUEST') !== false || strpos($key, 'SCRIPT') !== false || strpos($key, 'PATH') !== false || strpos($key, 'URI') !== false) {
        echo "<p><strong>{$key}:</strong> " . htmlspecialchars($value) . "</p>";
    }
}

echo "<h2>GET Variables</h2>";
if (!empty($_GET)) {
    foreach ($_GET as $key => $value) {
        echo "<p><strong>\$_GET['{$key}']:</strong> " . htmlspecialchars($value) . "</p>";
    }
} else {
    echo "<p>No GET variables</p>";
}

?>
