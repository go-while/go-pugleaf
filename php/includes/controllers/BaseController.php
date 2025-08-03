<?php
/**
 * Base Controller
 */

abstract class BaseController {
    protected $api;
    protected $template;

    public function __construct($api, $template) {
        $this->api = $api;
        $this->template = $template;

        // Set common template data
        $this->template->setData('site_name', 'PugLeaf');
        $this->template->setData('base_url', getBaseUrl());
    }

    /**
     * Render template with error handling
     */
    protected function render($template, $data = []) {
        try {
            $this->template->render($template, $data);
        } catch (Exception $e) {
            $this->renderError('Template Error', $e->getMessage());
        }
    }

    /**
     * Render error page
     */
    protected function renderError($title, $message, $code = 500) {
        http_response_code($code);

        try {
            $this->template->render('error', [
                'title' => $title,
                'message' => $message,
                'error_code' => $code
            ]);
        } catch (Exception $e) {
            // Fallback error display
            echo "<h1>Error</h1><p>{$title}: {$message}</p>";
        }
        exit;
    }

    /**
     * Get pagination parameters
     */
    protected function getPaginationParams() {
        $page = max(1, (int)($_GET['page'] ?? 1));
        $pageSize = min(50, max(1, (int)($_GET['page_size'] ?? 20)));

        return [$page, $pageSize];
    }

    /**
     * Build breadcrumb from path
     */
    protected function buildBreadcrumbFromPath($items) {
        $breadcrumb = [
            ['title' => 'Home', 'url' => '/']
        ];

        foreach ($items as $item) {
            $breadcrumb[] = $item;
        }

        return $breadcrumb;
    }
}
