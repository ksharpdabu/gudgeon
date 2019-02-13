// DataTable Config
$("#table1").DataTable({
  columns: [
    { data: null,
      className: "table-view-pf-select",
      render: function (data, type, full, meta) {
        // Select row checkbox renderer
        var id = "select" + meta.row;
        return '<label class="sr-only" for="' + id + '">Select row ' + meta.row +
          '</label><input type="checkbox" id="' + id + '" name="' + id + '">';
      },
      sortable: false
    },
    { data: "engine" },
    { data: "browser" },
    { data: "platforms" },
    { data: "version" },
    { data: "grade"},
    { data: null,
      className: "table-view-pf-actions",
      render: function (data, type, full, meta) {
        // Inline action button renderer
        return '<div class="table-view-pf-btn"><button class="btn btn-default" type="button">Actions</button></div>';
      }
    }, {
      data: null,
      className: "table-view-pf-actions",
      render: function (data, type, full, meta) {
        // Inline action kebab renderer
        return '<div class="dropdown dropdown-kebab-pf">' +
          '<button class="btn btn-default dropdown-toggle" type="button" data-toggle="dropdown" aria-haspopup="true" aria-expanded="true">' +
          '<span class="fa fa-ellipsis-v"></span></button>' +
          '<ul class="dropdown-menu dropdown-menu-right" aria-labelledby="dropdownKebabRight">' +
          '<li><a href="#">Action</a></li>' +
          '<li><a href="#">Another action</a></li>' +
          '<li><a href="#">Something else here</a></li>' +
          '<li role="separator" class="divider"></li>' +
          '<li><a href="#">Separated link</a></li></ul></div>';
      }
    }
  ],
  data: [],
  dom: "t",
  language: {
    zeroRecords: "No records found"
  },
  order: [[ 1, 'asc' ]],
  pfConfig: {
    emptyStateSelector: "#emptyState1",
    filterCaseInsensitive: true,
    filterCols: [
      null,
      {
        default: true,
        optionSelector: "#filter1",
        placeholder: "Filter By Rendering Engine..."
      }, {
        optionSelector: "#filter2",
        placeholder: "Filter By Browser..."
      }, {
        optionSelector: "#filter3",
        placeholder: "Filter By Platform(s)..."
      }, {
        optionSelector: "#filter4",
        placeholder: "Filter By Engine Version..."
      }, {
        optionSelector: "#filter5",
        placeholder: "Filter By CSS Grade..."
      }
    ],
    paginationSelector: "#pagination1",
    toolbarSelector: "#toolbar1",
    selectAllSelector: 'th:first-child input[type="checkbox"]',
    colvisMenuSelector: '.table-view-pf-colvis-menu'
  },
  select: {
    selector: 'td:first-child input[type="checkbox"]',
    style: 'multi'
  },
});
