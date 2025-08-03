<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title><?= isset($title) ? h($title) . ' - ' : '' ?><?= h($site_name ?? 'PugLeaf') ?></title>

    <!-- Bootstrap CSS -->
    <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/css/bootstrap.min.css" rel="stylesheet">

    <!-- Custom styles -->
    <style>
        .article-meta { font-size: 0.9em; color: #6c757d; }
        .article-body { white-space: pre-wrap; }
        .thread-level-1 { margin-left: 20px; }
        .thread-level-2 { margin-left: 40px; }
        .thread-level-3 { margin-left: 60px; }
        .navbar-brand img { height: 32px; }
        .stats-card { transition: transform 0.2s; }
        .stats-card:hover { transform: translateY(-2px); }
    </style>
</head>
<body>
    <!-- Navigation -->
    <nav class="navbar navbar-expand-lg navbar-dark bg-primary">
        <div class="container">
            <a class="navbar-brand" href="/">
                <strong><?= h($site_name ?? 'PugLeaf') ?></strong>
            </a>

            <button class="navbar-toggler" type="button" data-bs-toggle="collapse" data-bs-target="#navbarNav">
                <span class="navbar-toggler-icon"></span>
            </button>

            <div class="collapse navbar-collapse" id="navbarNav">
                <ul class="navbar-nav me-auto">
                    <li class="nav-item">
                        <a class="nav-link" href="/">Home</a>
                    </li>
                    <li class="nav-item">
                        <a class="nav-link" href="/groups">Groups</a>
                    </li>
                    <li class="nav-item">
                        <a class="nav-link" href="/search">Search</a>
                    </li>
                    <li class="nav-item">
                        <a class="nav-link" href="/stats">Stats</a>
                    </li>
                </ul>

                <form class="d-flex" action="/search" method="GET">
                    <input class="form-control me-2" type="search" placeholder="Search..." name="q" value="<?= h($_GET['q'] ?? '') ?>">
                    <button class="btn btn-outline-light" type="submit">Search</button>
                </form>
            </div>
        </div>
    </nav>

    <!-- Breadcrumb -->
    <?php if (!empty($breadcrumb)): ?>
    <div class="container mt-3">
        <?= buildBreadcrumb($breadcrumb) ?>
    </div>
    <?php endif; ?>

    <!-- Main content -->
    <div class="container mt-4">
        <?php include __DIR__ . "/{$template}.content.php"; ?>
    </div>

    <!-- Footer -->
    <footer class="bg-light mt-5 py-4">
        <div class="container">
            <div class="row">
                <div class="col-md-6">
                    <p class="mb-0">&copy; <?= date('Y') ?> <?= h($site_name ?? 'PugLeaf') ?></p>
                    <small class="text-muted">Modern NNTP Reader</small>
                </div>
                <div class="col-md-6 text-md-end">
                    <small class="text-muted">
                        Powered by <a href="https://github.com/go-while/go-pugleaf" target="_blank">go-pugleaf</a>
                    </small>
                </div>
            </div>
        </div>
    </footer>

    <!-- Bootstrap JS -->
    <script src="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/js/bootstrap.bundle.min.js"></script>
</body>
</html>
