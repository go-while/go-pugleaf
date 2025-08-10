<?php
/**
 * Search page template
 */
$template = 'base';
?>

<div class="row">
    <div class="col-lg-8">
        <h1>Search Articles</h1>

        <!-- Search Form -->
        <div class="card mb-4">
            <div class="card-body">
                <form method="GET" action="/search">
                    <div class="row">
                        <div class="col-md-8 mb-3">
                            <input type="text" class="form-control" name="q"
                                   placeholder="Search for articles..."
                                   value="<?= h($query) ?>" required>
                        </div>
                        <div class="col-md-4 mb-3">
                            <select class="form-select" name="group">
                                <option value="">All groups</option>
                                <!-- Add group options here if needed -->
                            </select>
                        </div>
                    </div>
                    <div class="row">
                        <div class="col-md-6">
                            <button type="submit" class="btn btn-primary">
                                <i class="bi bi-search"></i> Search
                            </button>
                        </div>
                    </div>
                </form>
            </div>
        </div>

        <?php if ($error): ?>
        <div class="alert alert-danger">
            <strong>Search Error:</strong> <?= h($error) ?>
        </div>
        <?php endif; ?>

        <?php if ($result): ?>
        <h3>Search Results</h3>

        <?php if (!empty($result['articles'])): ?>
        <div class="table-responsive">
            <table class="table table-hover">
                <thead>
                    <tr>
                        <th>Subject</th>
                        <th>Group</th>
                        <th>From</th>
                        <th>Date</th>
                    </tr>
                </thead>
                <tbody>
                    <?php foreach ($result['articles'] as $article): ?>
                    <tr>
                        <td>
                            <?php if (!empty($article['group_name']) && !empty($article['article_number'])): ?>
                            <a href="/groups/<?= urlencode($article['group_name']) ?>/articles/<?= (int)$article['article_number'] ?>"
                               class="text-decoration-none">
                                <?= h($article['subject'] ?? '[No Subject]') ?>
                            </a>
                            <?php elseif (!empty($article['message_id'])): ?>
                            <a href="/articles/<?= urlencode($article['message_id']) ?>"
                               class="text-decoration-none">
                                <?= h($article['subject'] ?? '[No Subject]') ?>
                            </a>
                            <?php else: ?>
                            <?= h($article['subject'] ?? '[No Subject]') ?>
                            <?php endif; ?>
                        </td>
                        <td>
                            <?php if (!empty($article['group_name'])): ?>
                            <a href="/groups/<?= urlencode($article['group_name']) ?>" class="text-decoration-none">
                                <?= h($article['group_name']) ?>
                            </a>
                            <?php else: ?>
                            <span class="text-muted">Unknown</span>
                            <?php endif; ?>
                        </td>
                        <td>
                            <small><?= h(extractDisplayName($article['from_header'] ?? '')) ?></small>
                        </td>
                        <td>
                            <small class="text-muted">
                                <?= formatRelativeTime($article['date'] ?? $article['article_date']) ?>
                            </small>
                        </td>
                    </tr>
                    <?php endforeach; ?>
                </tbody>
            </table>
        </div>

        <?php if (!empty($result['pagination']) && $result['pagination']['total_pages'] > 1): ?>
        <div class="d-flex justify-content-center">
            <?= pagination($result['pagination']['current_page'], $result['pagination']['total_pages'], '/search', ['q' => $query, 'group' => $group, 'page_size' => $page_size]) ?>
        </div>
        <?php endif; ?>

        <?php else: ?>
        <div class="alert alert-info">
            <h4>No Results Found</h4>
            <p>No articles found matching your search criteria.</p>
        </div>
        <?php endif; ?>

        <?php elseif ($query): ?>
        <div class="alert alert-warning">
            <h4>Search In Progress</h4>
            <p>Your search is being processed. Please wait...</p>
        </div>
        <?php endif; ?>
    </div>

    <div class="col-lg-4">
        <div class="card">
            <div class="card-header">
                <h5>Search Tips</h5>
            </div>
            <div class="card-body">
                <ul class="list-unstyled">
                    <li><strong>Phrase search:</strong> Use quotes "exact phrase"</li>
                    <li><strong>Wildcard:</strong> Use * for partial matches</li>
                    <li><strong>Boolean:</strong> AND, OR, NOT operators</li>
                    <li><strong>Group filter:</strong> Limit search to specific groups</li>
                </ul>
            </div>
        </div>

        <?php if ($query && $result): ?>
        <div class="card mt-3">
            <div class="card-header">
                <h6>Search Statistics</h6>
            </div>
            <div class="card-body">
                <p><strong>Query:</strong> <?= h($query) ?></p>
                <?php if ($group): ?>
                <p><strong>Group:</strong> <?= h($group) ?></p>
                <?php endif; ?>
                <p><strong>Results:</strong> <?= number_format($result['total_count'] ?? 0) ?></p>
            </div>
        </div>
        <?php endif; ?>
    </div>
</div>
