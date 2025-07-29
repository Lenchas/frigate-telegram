<!-- Created by AI Cursor -->
# Изменения для фильтрации по severity

## Добавлена поддержка фильтрации событий по severity

### Изменения в коде:

1. **Обновлена структура данных событий** (`internal/frigate/frigate.go`):
   - Добавлено поле `MaxSeverity` в структуры `EventsStruct` и `EventStruct`
   - Поле соответствует JSON полю `max_severity` из API Frigate

2. **Добавлена конфигурационная опция** (`internal/config/config.go`):
   - Добавлено поле `FrigateIncludeSeverity` в структуру `Config`
   - Добавлена инициализация из переменной окружения `FRIGATE_INCLUDE_SEVERITY`

3. **Реализована фильтрация** (`internal/frigate/frigate.go`):
   - Добавлена проверка severity в функции `ParseEvents`
   - События с severity, не входящим в список разрешенных, пропускаются

4. **Обновлена конфигурация** (`docker-compose.yml`):
   - Добавлена переменная окружения `FRIGATE_INCLUDE_SEVERITY: "alert"`

5. **Обновлена документация** (`README.md`):
   - Добавлено описание новой переменной окружения

### Использование:

Для получения уведомлений только по событиям с severity "alert":
```yaml
FRIGATE_INCLUDE_SEVERITY: "alert"
```

Для получения уведомлений по событиям с severity "alert" и "detection":
```yaml
FRIGATE_INCLUDE_SEVERITY: "alert,detection"
```

Для получения всех событий (по умолчанию):
```yaml
FRIGATE_INCLUDE_SEVERITY: "All"
```

### Возможные значения severity:
- `alert` - события с максимальной важностью
- `detection` - обычные события обнаружения 