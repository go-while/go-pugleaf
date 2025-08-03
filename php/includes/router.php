<?php
/**
 * Simple Router for PHP Frontend
 * Clean routing without dependencies
 */

class SimpleRouter {
    private $routes = [];
    private $currentRoute = null;

    /**
     * Add route
     */
    public function add($method, $pattern, $handler) {
        $this->routes[] = [
            'method' => strtoupper($method),
            'pattern' => $pattern,
            'handler' => $handler,
            'params' => []
        ];
    }

    /**
     * Dispatch request
     */
    public function dispatch() {
        $method = $_SERVER['REQUEST_METHOD'];
        $path = parse_url($_SERVER['REQUEST_URI'], PHP_URL_PATH);
        $path = rtrim($path, '/') ?: '/';

        foreach ($this->routes as $route) {
            if ($route['method'] !== $method) {
                continue;
            }

            if ($this->matchRoute($route['pattern'], $path, $params)) {
                $this->currentRoute = $route;
                $this->executeHandler($route['handler'], $params);
                return;
            }
        }

        // 404
        $this->render404();
    }

    /**
     * Match route pattern
     */
    private function matchRoute($pattern, $path, &$params) {
        $params = [];

        // Convert pattern to regex
        $regex = preg_quote($pattern, '#');
        $regex = preg_replace('/\\\{([^}]+)\\\}/', '([^/]+)', $regex);
        $regex = '#^' . $regex . '$#';

        if (preg_match($regex, $path, $matches)) {
            array_shift($matches); // Remove full match

            // Extract parameter names
            preg_match_all('/\{([^}]+)\}/', $pattern, $paramNames);
            if (isset($paramNames[1])) {
                foreach ($paramNames[1] as $index => $name) {
                    if (isset($matches[$index])) {
                        $params[$name] = $matches[$index];
                    }
                }
            }

            return true;
        }

        return false;
    }

    /**
     * Execute handler
     */
    private function executeHandler($handler, $params) {
        global $api, $template;

        if (is_string($handler) && strpos($handler, '@') !== false) {
            list($controllerName, $methodName) = explode('@', $handler);

            $controllerFile = __DIR__ . '/controllers/' . $controllerName . '.php';
            if (!file_exists($controllerFile)) {
                throw new Exception("Controller file not found: {$controllerFile}");
            }

            require_once $controllerFile;

            if (!class_exists($controllerName)) {
                throw new Exception("Controller class not found: {$controllerName}");
            }

            $controller = new $controllerName($api, $template);

            if (!method_exists($controller, $methodName)) {
                throw new Exception("Controller method not found: {$controllerName}@{$methodName}");
            }

            return $controller->$methodName($params);
        }

        if (is_callable($handler)) {
            return call_user_func($handler, $params);
        }

        throw new Exception("Invalid route handler");
    }

    /**
     * Render 404
     */
    private function render404() {
        global $template;

        http_response_code(404);
        $template->render('404', [
            'title' => 'Page Not Found',
            'message' => 'The requested page could not be found.'
        ]);
        exit;
    }

    /**
     * Get current route info
     */
    public function getCurrentRoute() {
        return $this->currentRoute;
    }
}
