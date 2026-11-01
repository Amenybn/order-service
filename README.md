# QuickShop · order-service

Microservice **commandes** de QuickShop (Go 1.23, bibliothèque standard uniquement).
Service **interne** : à chaque commande, il appelle **catalog-service** pour réserver le stock.
Si une réservation échoue, il annule celles déjà faites (compensation).

```bash
docker build -t order-service .
docker run -d -p 8002:8002 -e CATALOG_URL=http://<catalog-service>:8001 order-service
```

Les tests Go (`go test`) sont exécutés pendant le `docker build`.

| Méthode | Route | Rôle |
|---|---|---|
| GET | `/health`, `/info` | Supervision |
| GET / POST | `/orders` (`?notified=false`) | Lister / créer une commande |
| GET | `/orders/{id}` | Détail |
| POST | `/orders/{id}/cancel` | Annuler (le stock est remis au catalogue) |
| POST | `/orders/{id}/notification` | Appelé par notification-worker |
| POST / GET | `/workers/heartbeat` · `/workers` | Signal de vie du worker |

| Variable | Défaut | Rôle |
|---|---|---|
| `PORT` | `8002` | Port HTTP |
| `CATALOG_URL` | `http://catalog-service:8001` | URL interne de catalog-service |
| `CATALOG_TIMEOUT_MS` | `5000` | Délai maximal d'un appel au catalogue |

Utilisateur non-root `65532:65532`, aucune écriture disque. Données en mémoire.
