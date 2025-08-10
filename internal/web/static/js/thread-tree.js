// Thread Tree View JavaScript
// Handles expand/collapse functionality and dynamic loading

class ThreadTree {
    constructor(containerElement, options = {}) {
        this.container = containerElement;
        this.options = {
            autoCollapse: true,
            maxDepth: 0, // 0 = no limit
            collapseDepth: 3,
            apiEndpoint: '/api/thread-tree',
            ...options
        };

        this.threadRoot = containerElement.dataset.threadRoot;
        this.groupName = containerElement.dataset.groupName;

        this.init();
    }

    init() {
        // Mark that JavaScript is enabled
        this.container.classList.add('js-enabled');
        document.documentElement.classList.add('js-enabled');

        // For details/summary elements, prevent default behavior when JS is enabled
        this.disableNativeDetailsToggle();

        this.attachEventListeners();
        this.applyInitialCollapseState();
    }

    disableNativeDetailsToggle() {
        // Find all details elements and prevent their default toggle behavior
        const detailsElements = this.container.querySelectorAll('.tree-details');
        detailsElements.forEach(details => {
            details.addEventListener('toggle', (e) => {
                e.preventDefault();
                return false;
            });

            // Ensure all details are open so JS can control visibility
            details.setAttribute('open', 'true');

            // Mark parent node as expanded initially
            const treeNode = details.closest('.tree-node');
            if (treeNode && treeNode.classList.contains('expandable')) {
                treeNode.classList.add('expanded');
                treeNode.classList.remove('collapsed');
            }
        });
    }

    attachEventListeners() {
        // Handle expand/collapse clicks
        this.container.addEventListener('click', (e) => {
            const toggle = e.target.closest('.node-toggle, .toggle-icon');
            if (toggle) {
                e.preventDefault();
                e.stopPropagation();
                this.toggleNode(toggle.closest('.tree-node'));
            }
        });

        // Handle keyboard navigation
        this.container.addEventListener('keydown', (e) => {
            this.handleKeyNavigation(e);
        });

        // Make tree focusable for keyboard navigation
        this.container.setAttribute('tabindex', '0');
    }

    toggleNode(nodeElement) {
        const isCollapsed = nodeElement.classList.contains('collapsed');
        const detailsElement = nodeElement.querySelector('.tree-details');

        if (isCollapsed) {
            this.expandNode(nodeElement);
        } else {
            this.collapseNode(nodeElement);
        }
    }

    expandNode(nodeElement) {
        nodeElement.classList.remove('collapsed');
        nodeElement.classList.add('expanded');

        // Also handle details element
        const detailsElement = nodeElement.querySelector('.tree-details');
        if (detailsElement) {
            detailsElement.setAttribute('open', 'true');
        }

        // Trigger custom event
        this.container.dispatchEvent(new CustomEvent('nodeExpanded', {
            detail: {
                articleNum: nodeElement.dataset.articleNum,
                depth: parseInt(nodeElement.dataset.depth)
            }
        }));
    }

    collapseNode(nodeElement) {
        nodeElement.classList.remove('expanded');
        nodeElement.classList.add('collapsed');

        // Also handle details element
        const detailsElement = nodeElement.querySelector('.tree-details');
        if (detailsElement) {
            detailsElement.removeAttribute('open');
        }

        // Trigger custom event
        this.container.dispatchEvent(new CustomEvent('nodeCollapsed', {
            detail: {
                articleNum: nodeElement.dataset.articleNum,
                depth: parseInt(nodeElement.dataset.depth)
            }
        }));
    }

    applyInitialCollapseState() {
        if (!this.options.autoCollapse) return;

        const nodes = this.container.querySelectorAll('.tree-node.expandable');
        nodes.forEach(node => {
            const depth = parseInt(node.dataset.depth);
            if (depth >= this.options.collapseDepth) {
                this.collapseNode(node);
            } else {
                this.expandNode(node);
            }
        });
    }

    handleKeyNavigation(e) {
        const focusedNode = document.activeElement.closest('.tree-node');
        if (!focusedNode) return;

        switch (e.key) {
            case 'ArrowRight':
                e.preventDefault();
                if (focusedNode.classList.contains('collapsed')) {
                    this.expandNode(focusedNode);
                } else {
                    // Focus first child
                    const firstChild = focusedNode.querySelector('.tree-children > .tree-node');
                    if (firstChild) firstChild.focus();
                }
                break;

            case 'ArrowLeft':
                e.preventDefault();
                if (focusedNode.classList.contains('expanded')) {
                    this.collapseNode(focusedNode);
                } else {
                    // Focus parent
                    const parent = focusedNode.closest('.tree-children')?.closest('.tree-node');
                    if (parent) parent.focus();
                }
                break;

            case 'ArrowDown':
                e.preventDefault();
                this.focusNextNode(focusedNode);
                break;

            case 'ArrowUp':
                e.preventDefault();
                this.focusPreviousNode(focusedNode);
                break;

            case 'Home':
                e.preventDefault();
                const rootNode = this.container.querySelector('.tree-root > .tree-node');
                if (rootNode) rootNode.focus();
                break;

            case 'End':
                e.preventDefault();
                const lastNode = this.getLastVisibleNode();
                if (lastNode) lastNode.focus();
                break;
        }
    }

    focusNextNode(currentNode) {
        const allVisibleNodes = this.getVisibleNodes();
        const currentIndex = allVisibleNodes.indexOf(currentNode);
        if (currentIndex >= 0 && currentIndex < allVisibleNodes.length - 1) {
            allVisibleNodes[currentIndex + 1].focus();
        }
    }

    focusPreviousNode(currentNode) {
        const allVisibleNodes = this.getVisibleNodes();
        const currentIndex = allVisibleNodes.indexOf(currentNode);
        if (currentIndex > 0) {
            allVisibleNodes[currentIndex - 1].focus();
        }
    }

    getVisibleNodes() {
        return Array.from(this.container.querySelectorAll('.tree-node')).filter(node => {
            // Check if node is visible (not inside collapsed parent)
            let parent = node.parentElement.closest('.tree-node');
            while (parent) {
                if (parent.classList.contains('collapsed')) {
                    return false;
                }
                parent = parent.parentElement.closest('.tree-node');
            }
            return true;
        });
    }

    getLastVisibleNode() {
        const visibleNodes = this.getVisibleNodes();
        return visibleNodes[visibleNodes.length - 1];
    }

    // Expand all nodes up to a certain depth
    expandToDepth(maxDepth) {
        const nodes = this.container.querySelectorAll('.tree-node.expandable');
        nodes.forEach(node => {
            const depth = parseInt(node.dataset.depth);
            if (depth < maxDepth) {
                this.expandNode(node);
            } else {
                this.collapseNode(node);
            }
        });
    }

    // Collapse all expandable nodes
    collapseAll() {
        // Handle both old structure (.tree-node.expandable) and new structure (<details>)
        const expandableNodes = this.container.querySelectorAll('.tree-node.expandable');
        expandableNodes.forEach(node => this.collapseNode(node));
    }

    // Expand all expandable nodes
    expandAll() {
        // Handle both old structure (.tree-node.expandable) and new structure (<details>)
        const expandableNodes = this.container.querySelectorAll('.tree-node.expandable');
        expandableNodes.forEach(node => this.expandNode(node));
    }

    // Find and highlight a specific article in the tree
    highlightArticle(articleNum) {
        const nodeElement = this.container.querySelector(`[data-article-num="${articleNum}"]`);
        if (nodeElement) {
            // Remove existing highlights
            this.container.querySelectorAll('.tree-node.highlighted').forEach(node => {
                node.classList.remove('highlighted');
            });

            // Add highlight
            nodeElement.classList.add('highlighted');

            // Expand path to this node
            this.expandPathToNode(nodeElement);

            // Scroll into view
            nodeElement.scrollIntoView({
                behavior: 'smooth',
                block: 'center'
            });

            return true;
        }
        return false;
    }

    expandPathToNode(nodeElement) {
        let current = nodeElement;
        while (current) {
            const parent = current.closest('.tree-children')?.closest('.tree-node');
            if (parent && parent.classList.contains('collapsed')) {
                this.expandNode(parent);
            }
            current = parent;
        }
    }

    // Get tree statistics
    getStats() {
        const allNodes = this.container.querySelectorAll('.tree-node');
        const expandableNodes = this.container.querySelectorAll('.tree-node.expandable');
        const collapsedNodes = this.container.querySelectorAll('.tree-node.collapsed');

        return {
            totalNodes: allNodes.length,
            expandableNodes: expandableNodes.length,
            collapsedNodes: collapsedNodes.length,
            expandedNodes: expandableNodes.length - collapsedNodes.length,
            maxDepth: Math.max(...Array.from(allNodes).map(n => parseInt(n.dataset.depth) || 0))
        };
    }

    // Load additional tree data via AJAX (for pagination or lazy loading)
    async loadTreeData(options = {}) {
        const params = new URLSearchParams({
            group: this.groupName,
            thread_root: this.threadRoot,
            include_overview: 'true',
            ...options
        });

        try {
            const response = await fetch(`${this.options.apiEndpoint}?${params}`);
            if (!response.ok) {
                throw new Error(`HTTP ${response.status}: ${response.statusText}`);
            }

            const data = await response.json();
            return data;
        } catch (error) {
            console.error('Failed to load tree data:', error);
            throw error;
        }
    }
}

// Tree control panel functionality
class TreeControls {
    constructor(treeInstance, controlsElement) {
        this.tree = treeInstance;
        this.controls = controlsElement;
        this.init();
    }

    init() {
        this.createControls();
        this.attachEventListeners();
    }

    createControls() {
        this.controls.innerHTML = `
            <div class="tree-controls">
                <div class="control-group">
                    <button class="btn-expand-all" title="Expand All">⊞</button>
                    <button class="btn-collapse-all" title="Collapse All">⊟</button>
                </div>
                <div class="control-group">
                    <label>Depth:
                        <select class="depth-selector">
                            <option value="0">All</option>
                            <option value="1">1</option>
                            <option value="2">2</option>
                            <option value="3" selected>3</option>
                            <option value="4">4</option>
                            <option value="5">5</option>
                        </select>
                    </label>
                </div>
                <div class="control-group">
                    <input type="search" class="article-search" placeholder="Find article...">
                </div>
                <div class="tree-info">
                    <span class="node-count"></span>
                </div>
            </div>
        `;

        this.updateInfo();
    }

    attachEventListeners() {
        // Expand all button
        this.controls.querySelector('.btn-expand-all').addEventListener('click', () => {
            this.tree.expandAll();
            this.updateInfo();
        });

        // Collapse all button
        this.controls.querySelector('.btn-collapse-all').addEventListener('click', () => {
            this.tree.collapseAll();
            this.updateInfo();
        });

        // Depth selector
        this.controls.querySelector('.depth-selector').addEventListener('change', (e) => {
            const depth = parseInt(e.target.value);
            if (depth > 0) {
                this.tree.expandToDepth(depth);
            } else {
                this.tree.expandAll();
            }
            this.updateInfo();
        });

        // Article search
        const searchInput = this.controls.querySelector('.article-search');
        let searchTimeout;
        searchInput.addEventListener('input', (e) => {
            clearTimeout(searchTimeout);
            searchTimeout = setTimeout(() => {
                this.searchArticle(e.target.value);
            }, 300);
        });

        // Listen to tree events
        this.tree.container.addEventListener('nodeExpanded', () => this.updateInfo());
        this.tree.container.addEventListener('nodeCollapsed', () => this.updateInfo());
    }

    updateInfo() {
        const stats = this.tree.getStats();
        const infoElement = this.controls.querySelector('.node-count');
        infoElement.textContent = `${stats.totalNodes} nodes, ${stats.expandedNodes} expanded`;
    }

    searchArticle(query) {
        if (!query.trim()) return;

        // Simple search in visible text content
        const nodes = this.tree.container.querySelectorAll('.tree-node');
        let found = false;

        nodes.forEach(node => {
            const content = node.textContent.toLowerCase();
            if (content.includes(query.toLowerCase())) {
                const articleNum = node.dataset.articleNum;
                if (this.tree.highlightArticle(articleNum)) {
                    found = true;
                    return;
                }
            }
        });

        if (!found) {
            // Using JSON.stringify to safely log user input
            console.log('Article not found:', JSON.stringify(query));
        }
    }
}

// Enhanced article preview functionality
class ArticlePreview {
    constructor() {
        this.initializePreviewHandlers();
    }

    initializePreviewHandlers() {
        document.addEventListener('click', (e) => {
            if (e.target.classList.contains('btn-preview')) {
                e.preventDefault();
                e.stopPropagation();
                this.togglePreview(e.target);
            }
        });

        // Handle node-link clicks for preview
        document.addEventListener('click', (e) => {
            const nodeLink = e.target.closest('.node-link');
            if (nodeLink && !e.target.closest('.node-actions')) {
                e.preventDefault();
                this.togglePreviewByNode(nodeLink);
            }
        });
    }

    async togglePreview(button) {
        const articleNum = button.dataset.article;
        const previewDiv = document.getElementById(`preview-${articleNum}`);

        if (previewDiv.style.display === 'none') {
            await this.showPreview(articleNum, previewDiv);
        } else {
            this.hidePreview(previewDiv);
        }
    }

    async togglePreviewByNode(nodeLink) {
        const articleNum = nodeLink.dataset.articleNum;
        const previewDiv = document.getElementById(`preview-${articleNum}`);

        if (previewDiv.style.display === 'none') {
            await this.showPreview(articleNum, previewDiv);
        } else {
            this.hidePreview(previewDiv);
        }
    }

    async showPreview(articleNum, previewDiv) {
        const groupName = document.querySelector('.thread-tree').dataset.groupName;

        previewDiv.style.display = 'block';
        const contentDiv = previewDiv.querySelector('.preview-content');
        contentDiv.innerHTML = '<div class="preview-loading">Loading article preview...</div>';

        try {
            const response = await fetch(`/api/v1/groups/${groupName}/articles/${articleNum}/preview`);

            if (response.ok) {
                const article = await response.json();
                this.renderArticlePreview(contentDiv, article);
            } else {
                contentDiv.innerHTML = '<div class="preview-error">Failed to load article preview</div>';
            }
        } catch (error) {
            console.error('Error loading article preview:', error);
            contentDiv.innerHTML = '<div class="preview-error">Error loading article preview</div>';
        }
    }

    hidePreview(previewDiv) {
        previewDiv.style.display = 'none';
    }

    renderArticlePreview(contentDiv, article) {
        const preview = article.body ? article.body.substring(0, 2000) + `<a href="/groups/${article.group}/articles/${article.article_num}">...</a>` : 'No content available';

        // Prepare the preview text with proper formatting
        const cleanPreview = this.formatPreviewText(preview);

        // Note: Data from API is already sanitized via Go's PrintSanitized() function
        // No need to re-escape HTML entities that are already properly encoded
        contentDiv.innerHTML = `
            <!--
            <div class="article-preview-actions">
                <a href="/groups/${article.group}/articles/${article.article_num}" class="btn btn-sm btn-primary">
                    Read Full Article
                </a>
            </div>
            -->
            <!--
            <div class="article-preview-header">
                <strong>Subject:</strong> ${article.subject || 'No subject'}<br>
                <strong>From:</strong> ${article.from || 'Unknown'}<br>
                <strong>Date:</strong> ${article.date || 'Unknown'}<br>
                <strong>Lines:</strong> ${article.lines || 0}
            </div>
            -->
            <div class="article-preview-body">
                <div class="preview-text">${cleanPreview}</div>
            </div>
        `;
    }

    formatPreviewText(text) {
        // Remove excessive whitespace and normalize line breaks
        let formatted = text.replace(/\r\n/g, '\n').replace(/\r/g, '\n');

        // Remove more than 2 consecutive newlines
        formatted = formatted.replace(/\n{3,}/g, '\n\n');

        // Trim leading/trailing whitespace
        formatted = formatted.trim();

        return formatted;
    }

    escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    getAPIToken() {
        // For now, return empty - you might need to implement API token handling
        // or make the preview endpoint public
        return '';
    }
}

// Auto-initialize trees when DOM is loaded
document.addEventListener('DOMContentLoaded', () => {
    // Initialize article preview functionality
    new ArticlePreview();

    const treeContainers = document.querySelectorAll('.thread-tree');

    treeContainers.forEach(container => {
        const tree = new ThreadTree(container);

        // Look for associated controls
        const controlsId = container.dataset.controls;
        if (controlsId) {
            const controlsElement = document.getElementById(controlsId);
            if (controlsElement) {
                new TreeControls(tree, controlsElement);
            }
        }

        // Store tree instance on the container for external access
        container.treeInstance = tree;
    });
});

// Export for module systems
if (typeof module !== 'undefined' && module.exports) {
    module.exports = { ThreadTree, TreeControls };
}
