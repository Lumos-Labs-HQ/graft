// Filter state
let filters = [];
let currentColumns = [];

function toggleFilters() {
    const panel = document.getElementById('filter-panel');
    const btn = document.getElementById('filter-btn');
    panel.classList.toggle('show');
    btn.classList.toggle('active');
}

function addFilterRow(logic = 'where', column = '', operator = 'equals', value = '') {
    const row = document.createElement('div');
    row.className = 'filter-row';
    
    const logicSelect = logic === 'where' ? 
        `<select class="filter-logic" disabled><option>where</option></select>` :
        `<select class="filter-logic"><option>and</option><option>or</option></select>`;
    
    const columnOptions = currentColumns.map(col => 
        `<option value="${col.name}" ${col.name === column ? 'selected' : ''}>${col.name}</option>`
    ).join('');
    
    row.innerHTML = `
        ${logicSelect}
        <select class="filter-column">${columnOptions}</select>
        <select class="filter-operator">
            <option value="equals" ${operator === 'equals' ? 'selected' : ''}>equals</option>
            <option value="not_equals" ${operator === 'not_equals' ? 'selected' : ''}>not equals</option>
            <option value="contains" ${operator === 'contains' ? 'selected' : ''}>contains</option>
            <option value="starts_with" ${operator === 'starts_with' ? 'selected' : ''}>starts with</option>
            <option value="ends_with" ${operator === 'ends_with' ? 'selected' : ''}>ends with</option>
            <option value="gt" ${operator === 'gt' ? 'selected' : ''}>greater than</option>
            <option value="lt" ${operator === 'lt' ? 'selected' : ''}>less than</option>
            <option value="gte" ${operator === 'gte' ? 'selected' : ''}>≥</option>
            <option value="lte" ${operator === 'lte' ? 'selected' : ''}>≤</option>
        </select>
        <input type="text" class="filter-value" value="${value}" placeholder="Value">
        <button class="filter-remove" onclick="this.parentElement.remove(); updateFilterCount();">✕</button>
    `;
    
    document.getElementById('filter-rows').appendChild(row);
    updateFilterCount();
}

function updateFilterCount() {
    const count = document.getElementById('filter-rows').children.length;
    const badge = document.getElementById('filter-count');
    if (count > 0) {
        badge.textContent = count;
        badge.style.display = 'block';
    } else {
        badge.style.display = 'none';
    }
}

function clearFilters() {
    document.getElementById('filter-rows').innerHTML = '';
    updateFilterCount();
    filters = [];
    if (state.currentTable) {
        loadTableData();
    }
}

function applyFilters() {
    const rows = document.getElementById('filter-rows').children;
    filters = [];
    
    for (let row of rows) {
        const logicSelect = row.querySelector('.filter-logic');
        const logic = logicSelect ? logicSelect.value : 'where';
        const column = row.querySelector('.filter-column').value;
        const operator = row.querySelector('.filter-operator').value;
        const value = row.querySelector('.filter-value').value;
        
        if (column && value) {
            filters.push({ logic, column, operator, value });
        }
    }
    
    toggleFilters();
    
    if (!state.data || !state.data.rows) {
        return;
    }
    
    const allRows = state.data.rows;
    let filteredRows = allRows;
    
    filters.forEach((filter, index) => {
        const { logic, column, operator, value } = filter;
        
        const matchesFilter = (row) => {
            const cellValue = String(row[column] || '').toLowerCase();
            const filterValue = value.toLowerCase();
            
            let result = false;
            switch (operator) {
                case 'equals':
                    result = cellValue === filterValue;
                    break;
                case 'not_equals':
                    result = cellValue !== filterValue;
                    break;
                case 'contains':
                    result = cellValue.includes(filterValue);
                    break;
                case 'starts_with':
                    result = cellValue.startsWith(filterValue);
                    break;
                case 'ends_with':
                    result = cellValue.endsWith(filterValue);
                    break;
                case 'gt':
                    result = parseFloat(cellValue) > parseFloat(filterValue);
                    break;
                case 'lt':
                    result = parseFloat(cellValue) < parseFloat(filterValue);
                    break;
                case 'gte':
                    result = parseFloat(cellValue) >= parseFloat(filterValue);
                    break;
                case 'lte':
                    result = parseFloat(cellValue) <= parseFloat(filterValue);
                    break;
                default:
                    result = true;
            }
            return result;
        };
        
        if (index === 0) {
            filteredRows = allRows.filter(matchesFilter);
        } else if (logic === 'and') {
            filteredRows = filteredRows.filter(matchesFilter);
        } else if (logic === 'or') {
            const orMatches = allRows.filter(matchesFilter);
            const combined = [...filteredRows, ...orMatches];
            filteredRows = combined.filter((row, idx, self) => 
                idx === self.findIndex(r => JSON.stringify(r) === JSON.stringify(row))
            );
        }
    });
    
    const filteredData = {
        ...state.data,
        rows: filteredRows,
        total: filteredRows.length
    };
    
    renderDataGrid(filteredData);
    document.getElementById('row-count').textContent = `${filteredRows.length} of ${allRows.length}`;
}

function openSQLModal() {
    document.getElementById('sql-modal').classList.add('show');
}

function closeSQLModal() {
    document.getElementById('sql-modal').classList.remove('show');
}

async function executeSQLQuery() {
    const query = document.getElementById('sql-query').value.trim();
    if (!query) {
        showModal('Validation', 'Please enter a SQL query', 'warning');
        return;
    }
    
    try {
        const res = await fetch('/api/sql', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ query })
        });
        
        const json = await res.json();
        
        if (json.success) {
            state.data = json.data;
            renderDataGrid(json.data);
            closeSQLModal();
            showModal('Query executed', `${json.data.rows.length} rows returned`, 'success');
        } else {
            showModal('Error', json.message || 'Failed to execute query', 'error');
        }
    } catch (err) {
        showModal('Error', err.message, 'error');
    }
}

const originalSelectTable = selectTable;
selectTable = async function(tableName) {
    await originalSelectTable(tableName);
    if (state.data && state.data.columns) {
        currentColumns = state.data.columns;
    }
};
function filterIndexItems() {
    const query = document.getElementById('search-tables').value.toLowerCase();
    const items = document.querySelectorAll('#tables-list .table-item');
    items.forEach(item => {
        const name = item.textContent.toLowerCase();
        item.style.display = name.includes(query) ? 'flex' : 'none';
    });
}

function showCreateTableForm() {
    window.location.href = '/schema#create-table';
}

// Branch Management
async function loadBranches() {
    try {
        const response = await fetch('/api/branches');
        const data = await response.json();
        
        const selector = document.getElementById('branch-selector');
        selector.innerHTML = '';
        
        data.branches.forEach(branch => {
            const option = document.createElement('option');
            option.value = branch.name;
            option.textContent = `${branch.name}${branch.is_default ? ' (default)' : ''}`;
            if (branch.name === data.current) {
                option.selected = true;
            }
            selector.appendChild(option);
        });
    } catch (error) {
        console.error('Failed to load branches:', error);
    }
}

async function switchBranch(branchName) {
    if (!branchName) return;
    
    try {
        const response = await fetch('/api/branches/switch', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ branch: branchName })
        });
        
        if (response.ok) {
            showNotification(`Switched to branch: ${branchName}`, 'success');
            location.reload(); // Reload to show data from new branch
        } else {
            showNotification('Failed to switch branch', 'error');
        }
    } catch (error) {
        console.error('Failed to switch branch:', error);
        showNotification('Failed to switch branch', 'error');
    }
}

// Load branches on page load
document.addEventListener('DOMContentLoaded', () => {
    loadBranches();
});
