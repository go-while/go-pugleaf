<?php
/**
 * Help Controller
 */

require_once __DIR__ . '/BaseController.php';

class HelpController extends BaseController {

    /**
     * Help page
     */
    public function index($params) {
        $breadcrumb = $this->buildBreadcrumbFromPath([
            ['title' => 'Help', 'url' => '/help']
        ]);

        $this->render('help', [
            'title' => 'Help',
            'breadcrumb' => $breadcrumb
        ]);
    }
}
